// Package demo drives a simulated scenario through the real pipeline in memory and
// shapes the results into views for the browser demo. It compiles to WebAssembly
// (cmd/tollgate-wasm), so the demo runs the same engine as the backend.
package demo

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/pipeline"
	"github.com/TECHMINEs/Tollgate/internal/sim"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

type Cents = tariff.Cents

// Session is one run of a scenario.
type Session struct {
	sc     sim.Scenario
	mem    *pipeline.Memory
	proc   *pipeline.Processor
	cursor int
	rows   []StreamRow
}

// Overrides changes a tenant's audit policy for a what-if run.
type Overrides map[string]audit.Policy

// NewSession generates the scenario and provisions its tenants.
func NewSession(cfg sim.Config, over Overrides) *Session {
	sc := sim.Generate(cfg)
	mem := pipeline.NewMemory()
	for _, t := range sc.Tenants {
		pol := t.Policy
		if p, ok := over[t.ID]; ok {
			pol = p
		}
		mem.AddTenant(t.ID, t.Name, pol)
	}
	return &Session{sc: sc, mem: mem, proc: &pipeline.Processor{Store: mem}}
}

// Meta describes the scenario.
type Meta struct {
	Config   sim.Config   `json:"config"`
	Tenants  []sim.Tenant `json:"tenants"`
	Messages int          `json:"messages"`
	Invoices int          `json:"invoices"`
	Cursor   int          `json:"cursor"`
}

func (s *Session) Meta() Meta {
	return Meta{Config: s.sc.Config, Tenants: s.sc.Tenants, Messages: len(s.sc.Messages), Invoices: len(s.sc.Truth), Cursor: s.cursor}
}

// StreamRow is one consumed message and what it changed.
type StreamRow struct {
	Seq        int          `json:"seq"`
	Partition  int          `json:"partition"`
	Offset     int64        `json:"offset"`
	TenantID   string       `json:"tenant_id"`
	Type       events.Type  `json:"type"`
	Source     string       `json:"source"`
	ShipmentID string       `json:"shipment_id"`
	EventID    string       `json:"event_id"`
	OccurredAt time.Time    `json:"occurred_at"`
	Redelivery bool         `json:"redelivery"`
	Dropped    bool         `json:"dropped"` // inbox recognised a duplicate
	Verdicts   []VerdictRef `json:"verdicts,omitempty"`
	Postings   int          `json:"postings"`
	Outbox     int          `json:"outbox"`
	Error      string       `json:"error,omitempty"`
	Detail     string       `json:"detail"`
}

// VerdictRef is a verdict issued while processing a message.
type VerdictRef struct {
	InvoiceID string         `json:"invoice_id"`
	Version   int            `json:"version"`
	Decision  audit.Decision `json:"decision"`
}

// Step processes up to n further messages.
func (s *Session) Step(n int) []StreamRow {
	var out []StreamRow
	for ; n > 0 && s.cursor < len(s.sc.Messages); n-- {
		m := s.sc.Messages[s.cursor]
		s.cursor++
		e := m.Envelope
		row := StreamRow{Seq: s.cursor, Partition: m.Partition, Offset: m.Offset, TenantID: e.TenantID, Type: e.Type,
			Source: e.Source, ShipmentID: e.ShipmentID, EventID: e.EventID, OccurredAt: e.OccurredAt, Redelivery: m.Redelivery,
			Detail: describe(e)}
		res, err := s.proc.Handle(context.Background(), e)
		if err != nil {
			row.Error = err.Error()
		}
		row.Dropped = res.Duplicate
		row.Postings, row.Outbox = len(res.Postings), len(res.Outbox)
		for _, v := range res.Verdicts {
			row.Verdicts = append(row.Verdicts, VerdictRef{InvoiceID: v.InvoiceID, Version: v.Version, Decision: v.Decision})
		}
		s.rows = append(s.rows, row)
		out = append(out, row)
	}
	return out
}

// Done reports whether every message has been consumed.
func (s *Session) Done() bool { return s.cursor >= len(s.sc.Messages) }

func describe(e events.Envelope) string {
	switch e.Type {
	case events.StopReported:
		var p events.StopPayload
		if err := jsonUnmarshal(e.Payload, &p); err == nil {
			return "stop " + itoa(p.StopSeq) + " " + string(p.Kind) + " " + p.At.Format("Jan 2 15:04")
		}
	case events.InvoiceReceived:
		var p events.InvoicePayload
		if err := jsonUnmarshal(e.Payload, &p); err == nil {
			return p.Invoice.Number + " · " + p.Invoice.BilledCents().String()
		}
	case events.ShipmentTendered:
		var p events.ShipmentPayload
		if err := jsonUnmarshal(e.Payload, &p); err == nil {
			return p.Shipment.CarrierID + " · " + itoa(p.Shipment.Miles) + " mi · " + itoa(len(p.Shipment.Stops)) + " stops"
		}
	case events.TariffPublished:
		var p events.TariffPayload
		if err := jsonUnmarshal(e.Payload, &p); err == nil {
			return p.Tariff.ID + " v" + itoa(p.Tariff.Version) + " · " + itoa(len(p.Tariff.Lanes)) + " lanes"
		}
	}
	return ""
}

// ---------------------------------------------------------------- dashboard

// KPIs are a tenant's headline numbers, from latest verdict versions and the ledger.
type KPIs struct {
	Invoices         int   `json:"invoices"`
	Audited          int   `json:"audited"`
	Held             int   `json:"held"`
	Billed           Cents `json:"billed_cents"`
	Approved         Cents `json:"approved_cents"`
	Disputed         Cents `json:"disputed_cents"`
	ExpectedRecovery Cents `json:"expected_recovery_cents"`
	Leakage          Cents `json:"leakage_cents"`
	Accrued          Cents `json:"accrued_cents"`
	Shipments        int   `json:"shipments"`
	Delivered        int   `json:"delivered"`
	ReAudits         int   `json:"re_audits"` // verdict versions that changed the decision or the amounts
}

// InvoiceRow is one line of the invoice queue.
type InvoiceRow struct {
	InvoiceID  string         `json:"invoice_id"`
	Number     string         `json:"number"`
	ShipmentID string         `json:"shipment_id"`
	CarrierID  string         `json:"carrier_id"`
	Decision   audit.Decision `json:"decision"`
	Billed     Cents          `json:"billed_cents"`
	Approved   Cents          `json:"approved_cents"`
	Disputed   Cents          `json:"disputed_cents"`
	Leakage    Cents          `json:"leakage_cents"`
	Recovery   Cents          `json:"expected_recovery_cents"`
	Versions   int            `json:"versions"`
	Changes    int            `json:"changes"` // versions whose decision or amounts differ from the one before
	Findings   []string       `json:"findings"`
	Reason     string         `json:"reason"`
	Injected   []string       `json:"injected"` // the simulator's answer key, shown in the demo only
}

// CarrierRow is a carrier scorecard line.
type CarrierRow struct {
	CarrierID   string `json:"carrier_id"`
	Invoices    int    `json:"invoices"`
	Clean       int    `json:"clean"`
	AccuracyPct int    `json:"accuracy_pct"` // clean / audited
	Billed      Cents  `json:"billed_cents"`
	Disputed    Cents  `json:"disputed_cents"`
	Leakage     Cents  `json:"leakage_cents"`
	Detention   Cents  `json:"detention_billed_cents"`
}

// FindingRow totals one finding type.
type FindingRow struct {
	Finding audit.Finding `json:"finding"`
	Lines   int           `json:"lines"`
	Cents   Cents         `json:"variance_cents"`
}

// Detection compares verdicts with the simulator's answer key.
type Detection struct {
	Injected     int `json:"injected"`
	Caught       int `json:"caught"`
	Clean        int `json:"clean"`
	CleanFlagged int `json:"clean_flagged"`
	Pending      int `json:"pending"`
}

// Dashboard is everything the main screen shows for one tenant.
type Dashboard struct {
	TenantID  string                   `json:"tenant_id"`
	Name      string                   `json:"name"`
	KPIs      KPIs                     `json:"kpis"`
	Decisions map[audit.Decision]int   `json:"decisions"`
	Invoices  []InvoiceRow             `json:"invoices"`
	Carriers  []CarrierRow             `json:"carriers"`
	Findings  []FindingRow             `json:"findings"`
	Balances  map[ledger.Account]Cents `json:"balances"`
	Journal   []ledger.Posting         `json:"journal"` // most recent first
	Detection Detection                `json:"detection"`
	Policy    audit.Policy             `json:"policy"`
	Stream    []StreamRow              `json:"stream"` // most recent first
	Progress  [2]int                   `json:"progress"`
}

// Dashboard builds the main view for a tenant.
func (s *Session) Dashboard(tenantID string, streamLimit int) (Dashboard, error) {
	snap, err := s.mem.Snapshot(tenantID)
	if err != nil {
		return Dashboard{}, err
	}
	d := Dashboard{TenantID: tenantID, Name: snap.Name, Decisions: map[audit.Decision]int{}, Policy: snap.Policy,
		Balances: ledger.Balances(snap.Postings), Progress: [2]int{s.cursor, len(s.sc.Messages)}}
	d.KPIs.Shipments = len(snap.Shipments)
	for _, sh := range snap.Shipments {
		tl := timeline.Build(sh.Stops, snap.Stops[sh.ID], time.Duration(snap.Policy.ConflictToleranceMinutes)*time.Minute)
		if tl.Complete() {
			d.KPIs.Delivered++
		}
	}
	d.KPIs.Accrued = -d.Balances[ledger.AccruedFreight]

	carriers := map[string]*CarrierRow{}
	findings := map[audit.Finding]*FindingRow{}
	for id, vs := range snap.Verdicts {
		v := vs[len(vs)-1]
		truth := s.sc.Truth[id]
		row := InvoiceRow{InvoiceID: id, Number: v.InvoiceNumber, ShipmentID: v.ShipmentID, CarrierID: v.CarrierID,
			Decision: v.Decision, Billed: v.BilledCents, Approved: v.ApprovedCents, Disputed: v.DisputedCents,
			Leakage: v.LeakageCents, Recovery: v.ExpectedRecoveryCents, Versions: len(vs)}
		for _, e := range truth.Errors {
			row.Injected = append(row.Injected, string(e))
		}
		c := carriers[v.CarrierID]
		if c == nil {
			c = &CarrierRow{CarrierID: v.CarrierID}
			carriers[v.CarrierID] = c
		}
		c.Invoices++
		c.Billed += v.BilledCents
		c.Disputed += v.DisputedCents
		c.Leakage += v.LeakageCents
		for _, l := range v.Lines {
			if l.Code == tariff.Detention {
				c.Detention += l.BilledCents
			}
			if l.Finding != audit.Match {
				row.Findings = append(row.Findings, string(l.Finding)+" "+string(l.Code))
				if row.Reason == "" {
					row.Reason = l.Reason
				}
				f := findings[l.Finding]
				if f == nil {
					f = &FindingRow{Finding: l.Finding}
					findings[l.Finding] = f
				}
				f.Lines++
				f.Cents += max(0, l.VarianceCents)
			}
		}
		if row.Reason == "" && len(v.Reasons) > 0 {
			row.Reason = v.Reasons[0]
		}

		d.KPIs.Invoices++
		d.Decisions[v.Decision]++
		for i := 1; i < len(vs); i++ {
			a, b := vs[i-1], vs[i]
			if a.Decision != b.Decision || a.ApprovedCents != b.ApprovedCents || a.DisputedCents != b.DisputedCents {
				row.Changes++
			}
		}
		d.KPIs.ReAudits += row.Changes
		if v.Decision == audit.Hold {
			d.KPIs.Held++
			d.Detection.Pending++
		} else {
			d.KPIs.Audited++
			d.KPIs.Billed += v.BilledCents
			d.KPIs.Approved += v.ApprovedCents
			d.KPIs.Disputed += v.DisputedCents
			d.KPIs.Leakage += v.LeakageCents
			d.KPIs.ExpectedRecovery += v.ExpectedRecoveryCents
			if v.Decision == audit.Approve {
				c.Clean++
			}
			switch {
			case len(truth.Errors) == 0:
				d.Detection.Clean++
				if v.Decision != audit.Approve {
					d.Detection.CleanFlagged++
				}
			default:
				d.Detection.Injected++
				if v.Decision != audit.Approve {
					d.Detection.Caught++
				}
			}
		}
		d.Invoices = append(d.Invoices, row)
	}
	slices.SortFunc(d.Invoices, func(a, b InvoiceRow) int { return cmp.Compare(b.InvoiceID, a.InvoiceID) })
	for _, c := range carriers {
		if audited := c.Invoices - countHeld(snap, c.CarrierID); audited > 0 {
			c.AccuracyPct = c.Clean * 100 / audited
		}
		d.Carriers = append(d.Carriers, *c)
	}
	slices.SortFunc(d.Carriers, func(a, b CarrierRow) int { return cmp.Compare(a.CarrierID, b.CarrierID) })
	for _, f := range findings {
		d.Findings = append(d.Findings, *f)
	}
	slices.SortFunc(d.Findings, func(a, b FindingRow) int { return cmp.Compare(b.Cents, a.Cents) })

	ps := snap.Postings
	for i := len(ps) - 1; i >= 0 && len(d.Journal) < 40; i-- {
		d.Journal = append(d.Journal, ps[i])
	}
	for i := len(s.rows) - 1; i >= 0 && len(d.Stream) < streamLimit; i-- {
		if s.rows[i].TenantID == tenantID {
			d.Stream = append(d.Stream, s.rows[i])
		}
	}
	return d, nil
}

func countHeld(snap pipeline.Snapshot, carrier string) int {
	n := 0
	for _, vs := range snap.Verdicts {
		if v := vs[len(vs)-1]; v.CarrierID == carrier && v.Decision == audit.Hold {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------- shipment detail

// EventRow is a stop event as reported, for the evidence table.
type EventRow struct {
	timeline.Event
	Used bool `json:"used"` // chosen as the arrival or departure
}

// ShipmentDetail is the drill-down for one shipment.
type ShipmentDetail struct {
	Shipment audit.Shipment             `json:"shipment"`
	Lane     *tariff.Lane               `json:"lane,omitempty"`
	Tariff   *tariff.Tariff             `json:"tariff,omitempty"`
	Timeline timeline.Timeline          `json:"timeline"`
	Events   []EventRow                 `json:"events"`
	Verdicts map[string][]audit.Verdict `json:"verdicts"`
	Expected Cents                      `json:"expected_cents"`
	Accrued  Cents                      `json:"accrued_cents"`
}

// Shipment builds the drill-down for one shipment.
func (s *Session) Shipment(tenantID, shipmentID string) (ShipmentDetail, error) {
	snap, err := s.mem.Snapshot(tenantID)
	if err != nil {
		return ShipmentDetail{}, err
	}
	var d ShipmentDetail
	found := false
	for _, sh := range snap.Shipments {
		if sh.ID == shipmentID {
			d.Shipment, found = sh, true
		}
	}
	if !found {
		return d, pipeline.ErrNotFound
	}
	for i := range snap.Tariffs {
		if t := snap.Tariffs[i]; t.ID == d.Shipment.TariffID && t.Version == d.Shipment.TariffVersion {
			d.Tariff = &t
			if l, ok := t.Lane(d.Shipment.LaneID); ok {
				d.Lane = &l
			}
		}
	}
	evs := snap.Stops[shipmentID]
	d.Timeline = timeline.Build(d.Shipment.Stops, evs, time.Duration(snap.Policy.ConflictToleranceMinutes)*time.Minute)
	used := map[string]bool{}
	for _, st := range d.Timeline.Stops {
		if st.Arrival != nil {
			used[st.Arrival.EventID] = true
		}
		if st.Departure != nil {
			used[st.Departure.EventID] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range evs {
		if !seen[e.EventID] {
			seen[e.EventID] = true
			d.Events = append(d.Events, EventRow{Event: e, Used: used[e.EventID]})
		}
	}
	slices.SortFunc(d.Events, func(a, b EventRow) int {
		if c := cmp.Compare(a.StopSeq, b.StopSeq); c != 0 {
			return c
		}
		return a.At.Compare(b.At)
	})
	d.Verdicts = map[string][]audit.Verdict{}
	for id, vs := range snap.Verdicts {
		if vs[0].ShipmentID == shipmentID {
			d.Verdicts[id] = vs
		}
	}
	d.Expected, _ = audit.ExpectedCost(d.Shipment, d.Tariff, d.Timeline, snap.Policy)
	for _, p := range snap.Postings {
		if p.JournalKey == ledger.AccrualKey(shipmentID) && p.Account == ledger.AccruedFreight {
			d.Accrued += p.Credit - p.Debit
		}
	}
	return d, nil
}

// ---------------------------------------------------------------- whole-run tools

// Totals summarises a finished run for one tenant.
type Totals struct {
	Decisions map[audit.Decision]int `json:"decisions"`
	KPIs      KPIs                   `json:"kpis"`
}

// WhatIf runs the whole scenario with a different policy for one tenant and returns
// that tenant's totals, so a manager can see the effect of a policy change before
// making it.
func WhatIf(cfg sim.Config, tenantID string, pol audit.Policy) (Totals, error) {
	s := NewSession(cfg, Overrides{tenantID: pol})
	for !s.Done() {
		s.Step(500)
	}
	d, err := s.Dashboard(tenantID, 0)
	return Totals{Decisions: d.Decisions, KPIs: d.KPIs}, err
}

// ReplayRun is one delivery order in a replay check.
type ReplayRun struct {
	Name        string `json:"name"`
	Messages    int    `json:"messages"`
	Dropped     int    `json:"dropped"`
	Versions    int    `json:"verdict_versions"`
	Fingerprint string `json:"fingerprint"` // over every final verdict hash and ledger balance
}

// ReplayCheck runs the same scenario in several delivery orders and fingerprints the
// final state of each. Matching fingerprints are the proof that order and redelivery
// do not change the outcome.
func ReplayCheck(cfg sim.Config) []ReplayRun {
	variants := []struct {
		name string
		mod  func(*sim.Config)
	}{
		{"Broker order", func(c *sim.Config) { c.Shuffled = false; c.DuplicatePct = 0 }},
		{"Fully shuffled", func(c *sim.Config) { c.Shuffled = true; c.DuplicatePct = 0 }},
		{"Shuffled, 40% redelivered", func(c *sim.Config) { c.Shuffled = true; c.DuplicatePct = 40 }},
		{"Broker order, 80% redelivered", func(c *sim.Config) { c.Shuffled = false; c.DuplicatePct = 80 }},
	}
	var out []ReplayRun
	for _, v := range variants {
		c := cfg
		v.mod(&c)
		s := NewSession(c, nil)
		run := ReplayRun{Name: v.name}
		for !s.Done() {
			for _, r := range s.Step(500) {
				run.Messages++
				if r.Dropped {
					run.Dropped++
				}
				run.Versions += len(r.Verdicts)
			}
		}
		run.Fingerprint = s.fingerprint()
		out = append(out, run)
	}
	return out
}

func (s *Session) fingerprint() string {
	var parts []string
	for _, t := range s.sc.Tenants {
		snap, _ := s.mem.Snapshot(t.ID)
		for id, vs := range snap.Verdicts {
			parts = append(parts, id+"="+vs[len(vs)-1].EvidenceHash+string(vs[len(vs)-1].Decision))
		}
		for a, b := range ledger.Balances(snap.Postings) {
			parts = append(parts, t.ID+":"+string(a)+"="+b.String())
		}
	}
	slices.Sort(parts)
	return shortHash(strings.Join(parts, "|"))
}
