// Package audit checks a carrier's freight invoice against the contracted tariff and the
// reconstructed stop timeline, and decides what to pay.
//
// Audit is a pure function. The verdict carries an evidence hash over everything it
// read, so the pipeline can tell a meaningful re-audit from a redelivered event.
package audit

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

type Cents = tariff.Cents

// Shipment is what the shipper tendered to the carrier.
type Shipment struct {
	TenantID          string          `json:"tenant_id"`
	ID                string          `json:"id"`
	CarrierID         string          `json:"carrier_id"`
	TariffID          string          `json:"tariff_id"`
	TariffVersion     int             `json:"tariff_version"`
	LaneID            string          `json:"lane_id"`
	Miles             int             `json:"miles"`
	FuelPriceCPG      int64           `json:"fuel_price_cpg"` // published diesel index for the ship week
	LiftgateRequested bool            `json:"liftgate_requested"`
	Stops             []timeline.Stop `json:"stops"`
	TenderedAt        time.Time       `json:"tendered_at"`
}

// InvoiceLine is one charge billed by the carrier.
type InvoiceLine struct {
	Code        tariff.ChargeCode `json:"code"`
	StopSeq     int               `json:"stop_seq,omitempty"`
	AmountCents Cents             `json:"amount_cents"`
	ReceiptRef  string            `json:"receipt_ref,omitempty"`
}

// Invoice is one revision of a carrier freight bill (EDI 210).
type Invoice struct {
	TenantID   string        `json:"tenant_id"`
	ID         string        `json:"id"`
	Number     string        `json:"number"` // carrier's invoice number
	Revision   int           `json:"revision"`
	CarrierID  string        `json:"carrier_id"`
	ShipmentID string        `json:"shipment_id"`
	Lines      []InvoiceLine `json:"lines"`
	ReceivedAt time.Time     `json:"received_at"`
}

// BilledCents is the invoice total.
func (inv Invoice) BilledCents() Cents {
	var t Cents
	for _, l := range inv.Lines {
		t += l.AmountCents
	}
	return t
}

// precedes orders invoices by when they reached the shipper; the earlier one owns a charge.
func precedes(a, b Invoice) bool {
	if c := a.ReceivedAt.Compare(b.ReceivedAt); c != 0 {
		return c < 0
	}
	return a.ID < b.ID
}

// Finding classifies one invoice line.
type Finding string

const (
	Match        Finding = "MATCH"
	Overbilled   Finding = "OVERBILLED"
	Underbilled  Finding = "UNDERBILLED"
	Unsupported  Finding = "UNSUPPORTED"  // the evidence the contract requires is missing
	Uncontracted Finding = "UNCONTRACTED" // the tariff has no such charge
	Duplicate    Finding = "DUPLICATE"    // already billed on an earlier invoice
	Pending      Finding = "PENDING"      // evidence has not arrived yet
)

// Decision is what accounts payable should do with the invoice.
type Decision string

const (
	Approve         Decision = "APPROVE"          // pay as billed, nothing wrong
	ApproveVariance Decision = "APPROVE_VARIANCE" // pay as billed; the overcharge costs less than disputing it
	ShortPay        Decision = "SHORT_PAY"        // pay the audited amount and dispute the rest
	Reject          Decision = "REJECT"           // duplicate invoice: pay nothing
	Hold            Decision = "HOLD"             // cannot rate yet
)

// Policy is a tenant's audit and dispute policy. It is data, versioned like a tariff.
type Policy struct {
	Version                  int   `json:"version"`
	LineToleranceCents       Cents `json:"line_tolerance_cents"`
	DisputeCostCents         Cents `json:"dispute_cost_cents"` // loaded cost of working one dispute
	RequireIndependentForDet bool  `json:"require_independent_detention"`
	ConflictToleranceMinutes int   `json:"conflict_tolerance_minutes"`
	// Assumed probability (basis points) that the carrier accepts a dispute, by finding.
	// These are tenant inputs to the dispute decision, not measured rates.
	WinBps map[Finding]int64 `json:"win_bps"`
	// WinBpsCarrierOnly replaces WinBps[Overbilled] when the only contrary evidence is
	// the carrier's own data.
	WinBpsCarrierOnly int64 `json:"win_bps_carrier_only"`
}

// DefaultPolicy is the starting policy for a new tenant.
func DefaultPolicy() Policy {
	return Policy{
		Version:                  1,
		LineToleranceCents:       100,
		DisputeCostCents:         3_500,
		RequireIndependentForDet: true,
		ConflictToleranceMinutes: 10,
		WinBps: map[Finding]int64{
			Overbilled:   9_000,
			Unsupported:  7_000,
			Uncontracted: 9_500,
			Duplicate:    9_800,
		},
		WinBpsCarrierOnly: 5_000,
	}
}

// LineResult is the audit of one invoice line.
type LineResult struct {
	LineNo        int               `json:"line_no"`
	Code          tariff.ChargeCode `json:"code"`
	StopSeq       int               `json:"stop_seq,omitempty"`
	BilledCents   Cents             `json:"billed_cents"`
	ExpectedCents Cents             `json:"expected_cents"`
	VarianceCents Cents             `json:"variance_cents"` // billed - expected
	Finding       Finding           `json:"finding"`
	Reason        string            `json:"reason"`
	Evidence      []string          `json:"evidence,omitempty"`
	WinBps        int64             `json:"win_bps,omitempty"`

	Detention *tariff.DetentionCalc `json:"detention,omitempty"`
}

// Verdict is the audited outcome of one invoice revision.
type Verdict struct {
	TenantID        string       `json:"tenant_id"`
	ShipmentID      string       `json:"shipment_id"`
	InvoiceID       string       `json:"invoice_id"`
	InvoiceNumber   string       `json:"invoice_number"`
	InvoiceRevision int          `json:"invoice_revision"`
	CarrierID       string       `json:"carrier_id"`
	Decision        Decision     `json:"decision"`
	Lines           []LineResult `json:"lines"`
	Reasons         []string     `json:"reasons,omitempty"`

	BilledCents           Cents `json:"billed_cents"`
	ExpectedCents         Cents `json:"expected_cents"`
	ApprovedCents         Cents `json:"approved_cents"` // to pay now
	DisputedCents         Cents `json:"disputed_cents"` // withheld and disputed
	LeakageCents          Cents `json:"leakage_cents"`  // overcharge knowingly paid
	ExpectedRecoveryCents Cents `json:"expected_recovery_cents"`

	EvidenceHash string `json:"evidence_hash"`
	Version      int    `json:"version"` // assigned by the pipeline when the verdict changes
}

// Input is everything Audit may read. Nothing else influences the verdict.
type Input struct {
	Shipment Shipment
	Tariff   *tariff.Tariff // nil until the tariff version has been received
	Timeline timeline.Timeline
	Invoice  Invoice
	Others   []Invoice // latest revision of every other invoice for this shipment
	Policy   Policy
}

// Audit rates every line and decides the invoice.
func Audit(in Input) Verdict {
	inv := in.Invoice
	v := Verdict{
		TenantID: inv.TenantID, ShipmentID: inv.ShipmentID, InvoiceID: inv.ID,
		InvoiceNumber: inv.Number, InvoiceRevision: inv.Revision, CarrierID: inv.CarrierID,
		BilledCents:  inv.BilledCents(),
		EvidenceHash: EvidenceHash(in),
	}

	if in.Tariff == nil {
		return hold(v, inv, fmt.Sprintf("tariff %s v%d not received yet", in.Shipment.TariffID, in.Shipment.TariffVersion))
	}
	if inv.CarrierID != in.Shipment.CarrierID {
		return hold(v, inv, fmt.Sprintf("invoice carrier %s is not the tendered carrier %s", inv.CarrierID, in.Shipment.CarrierID))
	}
	lane, ok := in.Tariff.Lane(in.Shipment.LaneID)
	if !ok {
		return hold(v, inv, fmt.Sprintf("lane %s not in tariff %s v%d", in.Shipment.LaneID, in.Tariff.ID, in.Tariff.Version))
	}

	// A re-sent invoice number from the same carrier is a duplicate of the earliest one.
	for _, o := range in.Others {
		if o.Number == inv.Number && o.CarrierID == inv.CarrierID && precedes(o, inv) {
			v.Decision = Reject
			v.Reasons = append(v.Reasons, fmt.Sprintf("invoice number %s already received as %s", inv.Number, o.ID))
			for i, l := range inv.Lines {
				v.Lines = append(v.Lines, LineResult{LineNo: i + 1, Code: l.Code, StopSeq: l.StopSeq, BilledCents: l.AmountCents,
					VarianceCents: l.AmountCents, Finding: Duplicate, Reason: "duplicate invoice", WinBps: in.Policy.WinBps[Duplicate]})
			}
			v.DisputedCents = v.BilledCents
			v.ExpectedRecoveryCents = Cents(int64(v.BilledCents) * in.Policy.WinBps[Duplicate] / 10_000)
			return v
		}
	}

	linehaul := tariff.LinehaulCents(lane, in.Shipment.Miles)
	r := rater{in: in, lane: lane, linehaul: linehaul, billedBefore: billedBefore(inv, in.Others)}
	for i, l := range inv.Lines {
		lr := r.rate(l)
		lr.LineNo = i + 1
		v.Lines = append(v.Lines, lr)
	}
	return decide(v, in.Policy)
}

func hold(v Verdict, inv Invoice, reason string) Verdict {
	v.Decision = Hold
	v.Reasons = append(v.Reasons, reason)
	for i, l := range inv.Lines {
		v.Lines = append(v.Lines, LineResult{LineNo: i + 1, Code: l.Code, StopSeq: l.StopSeq, BilledCents: l.AmountCents, Finding: Pending, Reason: reason})
	}
	return v
}

type lineKey struct {
	code tariff.ChargeCode
	stop int
}

// billedBefore lists the charges that an earlier invoice for the shipment already billed.
func billedBefore(inv Invoice, others []Invoice) map[lineKey]string {
	out := map[lineKey]string{}
	for _, o := range others {
		if o.CarrierID != inv.CarrierID || !precedes(o, inv) {
			continue
		}
		for _, l := range o.Lines {
			k := lineKey{l.Code, l.StopSeq}
			if _, ok := out[k]; !ok {
				out[k] = o.ID
			}
		}
	}
	return out
}

type rater struct {
	in           Input
	lane         tariff.Lane
	linehaul     Cents
	billedBefore map[lineKey]string
}

func (r rater) rate(l InvoiceLine) LineResult {
	res := LineResult{Code: l.Code, StopSeq: l.StopSeq, BilledCents: l.AmountCents}
	pol, tf, sh := r.in.Policy, r.in.Tariff, r.in.Shipment

	if prior, ok := r.billedBefore[lineKey{l.Code, l.StopSeq}]; ok && l.Code != tariff.Lumper {
		return finish(res, 0, Duplicate, fmt.Sprintf("already billed on invoice %s", prior), pol)
	}

	switch l.Code {
	case tariff.Linehaul:
		basis := fmt.Sprintf("flat %s", r.lane.FlatCents)
		if r.lane.RatePerMileCents > 0 {
			basis = fmt.Sprintf("%d mi x %s/mi", sh.Miles, r.lane.RatePerMileCents)
		}
		return compare(res, r.linehaul, fmt.Sprintf("lane %s: %s, minimum %s", r.lane.ID, basis, r.lane.MinimumCents), pol)

	case tariff.Fuel:
		amt, band, err := tariff.FuelCents(tf.Fuel, r.linehaul, sh.FuelPriceCPG)
		if err != nil {
			return finish(res, 0, Pending, fmt.Sprintf("diesel index %d cpg outside the fuel table", sh.FuelPriceCPG), pol)
		}
		return compare(res, amt, fmt.Sprintf("diesel %d cpg -> band %d-%d at %.2f%% of %s linehaul",
			sh.FuelPriceCPG, band.FromCPG, band.ToCPG, float64(band.SurchargeBps)/100, r.linehaul), pol)

	case tariff.Detention:
		st, ok := r.in.Timeline.Stop(l.StopSeq)
		if !ok {
			return finish(res, 0, Unsupported, fmt.Sprintf("shipment has no stop %d", l.StopSeq), pol)
		}
		if !st.Complete() {
			reason := fmt.Sprintf("stop %d: waiting for arrival and departure events", l.StopSeq)
			if st.Anomaly != "" {
				reason = fmt.Sprintf("stop %d: %s", l.StopSeq, st.Anomaly)
			}
			return finish(res, 0, Pending, reason, pol)
		}
		res.Evidence = []string{st.Arrival.EventID, st.Departure.EventID}
		independent := timeline.Independent(st.Arrival.Source) && timeline.Independent(st.Departure.Source)
		if pol.RequireIndependentForDet && !independent {
			return finish(res, 0, Unsupported, fmt.Sprintf("stop %d times are carrier-reported only; policy requires dock or ELD evidence", l.StopSeq), pol)
		}
		calc := tariff.DetentionCents(tf.Detention, st.Stop.Appointment, st.Arrival.At, st.Departure.At)
		res.Detention = &calc
		var reason string
		switch {
		case calc.Forfeited:
			reason = fmt.Sprintf("arrived %d min after the %s appointment; contract forfeits detention past %d min",
				calc.LateMinutes, st.Stop.Appointment.Format("15:04"), tf.Detention.LateGraceMinutes)
		default:
			reason = fmt.Sprintf("stop %d dwell %d min (%s->%s), %d free, %d x %d-min increments",
				l.StopSeq, calc.DwellMinutes, st.Arrival.Source, st.Departure.Source, tf.Detention.FreeMinutes, calc.Increments, tf.Detention.IncrementMinutes)
		}
		res = compare(res, calc.AmountCents, reason, pol)
		if res.Finding == Overbilled && !independent {
			res.WinBps = pol.WinBpsCarrierOnly
		}
		return res

	case tariff.Liftgate:
		if !sh.LiftgateRequested {
			return finish(res, 0, Unsupported, "liftgate was not requested on the tender", pol)
		}
		return compare(res, tf.LiftgateCents, "liftgate requested on tender", pol)

	case tariff.StopOff:
		extra := max(0, len(sh.Stops)-2)
		if extra == 0 {
			return finish(res, 0, Unsupported, "shipment has no intermediate stops", pol)
		}
		return compare(res, Cents(extra)*tf.StopOffCents, fmt.Sprintf("%d intermediate stop(s) x %s", extra, tf.StopOffCents), pol)

	case tariff.Lumper:
		if l.ReceiptRef == "" {
			return finish(res, 0, Unsupported, "lumper charge without a receipt reference", pol)
		}
		return compare(res, min(l.AmountCents, tf.LumperCapCents), fmt.Sprintf("receipt %s, cap %s", l.ReceiptRef, tf.LumperCapCents), pol)
	}
	return finish(res, 0, Uncontracted, fmt.Sprintf("%s is not a charge in tariff %s v%d", l.Code, tf.ID, tf.Version), pol)
}

func compare(res LineResult, expected Cents, reason string, pol Policy) LineResult {
	diff := res.BilledCents - expected
	switch {
	case diff > pol.LineToleranceCents:
		return finish(res, expected, Overbilled, reason, pol)
	case diff < -pol.LineToleranceCents:
		return finish(res, expected, Underbilled, reason, pol)
	}
	return finish(res, expected, Match, reason, pol)
}

func finish(res LineResult, expected Cents, f Finding, reason string, pol Policy) LineResult {
	res.ExpectedCents, res.Finding, res.Reason = expected, f, reason
	res.VarianceCents = res.BilledCents - expected
	if f == Match {
		res.VarianceCents = 0
	}
	if res.VarianceCents > 0 {
		res.WinBps = pol.WinBps[f]
	}
	return res
}

// decide turns line findings into a payment decision using the dispute economics:
// dispute only when the expected recovery exceeds the cost of working the dispute.
func decide(v Verdict, pol Policy) Verdict {
	var overcharge, recovery int64
	for _, l := range v.Lines {
		v.ExpectedCents += l.ExpectedCents
		if l.Finding == Pending {
			v.Decision = Hold
			v.Reasons = append(v.Reasons, l.Reason)
		}
		if l.VarianceCents > 0 { // an undercharge is paid as billed and never disputed
			overcharge += int64(l.VarianceCents)
			recovery += int64(l.VarianceCents) * l.WinBps
		}
	}
	if v.Decision == Hold {
		v.ExpectedCents = 0
		return v
	}
	v.ExpectedRecoveryCents = Cents(recovery / 10_000)
	switch {
	case overcharge == 0:
		v.Decision, v.ApprovedCents = Approve, v.BilledCents
	case v.ExpectedRecoveryCents > pol.DisputeCostCents:
		v.Decision = ShortPay
		v.DisputedCents = Cents(overcharge)
		v.ApprovedCents = v.BilledCents - v.DisputedCents
		v.Reasons = append(v.Reasons, fmt.Sprintf("expected recovery %s exceeds dispute cost %s", v.ExpectedRecoveryCents, pol.DisputeCostCents))
	default:
		v.Decision, v.ApprovedCents = ApproveVariance, v.BilledCents
		v.LeakageCents = Cents(overcharge)
		v.Reasons = append(v.Reasons, fmt.Sprintf("expected recovery %s does not cover dispute cost %s", v.ExpectedRecoveryCents, pol.DisputeCostCents))
	}
	return v
}

// EvidenceHash fingerprints every input to Audit. Equal hashes mean equal verdicts.
func EvidenceHash(in Input) string {
	var b strings.Builder
	tid, tver := in.Shipment.TariffID, in.Shipment.TariffVersion
	loaded := in.Tariff != nil
	fmt.Fprintf(&b, "shipment=%s/%s|tariff=%s@%d loaded=%t|policy=%d|invoice=%s@%d|events=",
		in.Shipment.TenantID, in.Shipment.ID, tid, tver, loaded, in.Policy.Version, in.Invoice.ID, in.Invoice.Revision)
	b.WriteString(strings.Join(in.Timeline.EventIDs, ","))
	others := make([]string, 0, len(in.Others))
	for _, o := range in.Others {
		others = append(others, fmt.Sprintf("%s@%d", o.ID, o.Revision))
	}
	slices.Sort(others)
	b.WriteString("|others=" + strings.Join(others, ","))
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ExpectedCost is what the shipment should cost under its tariff given the evidence so
// far. Finance accrues it at delivery, before the carrier's invoice arrives.
func ExpectedCost(sh Shipment, tf *tariff.Tariff, tl timeline.Timeline, pol Policy) (Cents, bool) {
	if tf == nil {
		return 0, false
	}
	lane, ok := tf.Lane(sh.LaneID)
	if !ok {
		return 0, false
	}
	linehaul := tariff.LinehaulCents(lane, sh.Miles)
	total := linehaul
	if fuel, _, err := tariff.FuelCents(tf.Fuel, linehaul, sh.FuelPriceCPG); err == nil {
		total += fuel
	}
	if sh.LiftgateRequested {
		total += tf.LiftgateCents
	}
	total += Cents(max(0, len(sh.Stops)-2)) * tf.StopOffCents
	for _, st := range tl.Stops {
		if !st.Complete() {
			continue
		}
		if pol.RequireIndependentForDet && !(timeline.Independent(st.Arrival.Source) && timeline.Independent(st.Departure.Source)) {
			continue
		}
		total += tariff.DetentionCents(tf.Detention, st.Stop.Appointment, st.Arrival.At, st.Departure.At).AmountCents
	}
	return total, true
}

// SortInvoices orders invoices canonically (by receipt, then ID).
func SortInvoices(invs []Invoice) {
	slices.SortFunc(invs, func(a, b Invoice) int {
		if c := a.ReceivedAt.Compare(b.ReceivedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}
