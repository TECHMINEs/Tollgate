// Package sim generates a deterministic, synthetic freight network: two shipper tenants,
// their carrier tariffs, shipments, multi-source stop events and carrier invoices with
// known injected billing errors.
//
// Everything here is invented test data. The same Config always produces the same
// messages, so tests, the CLI producer and the browser demo all see the same world.
package sim

import (
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

// Partitions is the partition count of the inbound topic, mirrored by the simulator.
const Partitions = 6

// Config controls a scenario.
type Config struct {
	Seed               uint64    `json:"seed"`
	ShipmentsPerTenant int       `json:"shipments_per_tenant"`
	ErrorRatePct       int       `json:"error_rate_pct"`      // share of invoices with an injected billing error
	DuplicatePct       int       `json:"duplicate_pct"`       // share of messages the "broker" redelivers
	LateTelematicsPct  int       `json:"late_telematics_pct"` // share of ELD batches that arrive after the invoice
	Shuffled           bool      `json:"shuffled"`            // ignore arrival times and deliver in random order
	Start              time.Time `json:"start"`
}

// DefaultConfig is the scenario the demo opens with.
func DefaultConfig() Config {
	return Config{Seed: 7, ShipmentsPerTenant: 24, ErrorRatePct: 30, DuplicatePct: 8, LateTelematicsPct: 15,
		Start: time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)}
}

// ErrorKind is a billing error injected into an invoice.
type ErrorKind string

const (
	DetentionInflated    ErrorKind = "detention_inflated"     // carrier reports a later departure than the dock/ELD
	DetentionLateArrival ErrorKind = "detention_late_arrival" // detention billed after arriving past the grace period
	DetentionNoEvidence  ErrorKind = "detention_no_evidence"  // detention backed only by the carrier's own times
	FuelWrongBand        ErrorKind = "fuel_wrong_band"
	RateTypo             ErrorKind = "linehaul_rate_typo"
	DuplicateInvoice     ErrorKind = "duplicate_invoice"
	UncontractedCharge   ErrorKind = "uncontracted_charge"
	LiftgateNotRequested ErrorKind = "liftgate_not_requested"
	LumperNoReceipt      ErrorKind = "lumper_no_receipt"
)

// Truth is the generator's answer key for one invoice.
type Truth struct {
	TenantID   string      `json:"tenant_id"`
	ShipmentID string      `json:"shipment_id"`
	InvoiceID  string      `json:"invoice_id"`
	Errors     []ErrorKind `json:"errors,omitempty"`
}

// Message is an envelope as the broker would deliver it.
type Message struct {
	Partition  int             `json:"partition"`
	Offset     int64           `json:"offset"`
	Redelivery bool            `json:"redelivery"`
	Envelope   events.Envelope `json:"envelope"`
}

// Tenant is a shipper using Tollgate.
type Tenant struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Policy audit.Policy `json:"policy"`
}

// Scenario is a generated world.
type Scenario struct {
	Config   Config           `json:"config"`
	Tenants  []Tenant         `json:"tenants"`
	Messages []Message        `json:"messages"`
	Truth    map[string]Truth `json:"truth"` // by invoice ID
}

type carrier struct{ id, prefix string }

type laneDef struct {
	origin, dest string
	miles        int
}

type tenantDef struct {
	id, short, name, dc string
	carriers            []carrier
	lanes               []laneDef
	lumperPct           int
}

// Fictional shippers and carriers. Real city names give the lanes some texture.
var tenantDefs = []tenantDef{
	{
		id: "larchmont-grocers", short: "lg", name: "Larchmont Grocers", dc: "Larchmont DC Columbus",
		carriers: []carrier{{"ironbridge-freight", "IBF"}, {"pellston-transport", "PTX"}},
		lanes: []laneDef{
			{"Columbus, OH", "Indianapolis, IN", 176}, {"Columbus, OH", "Pittsburgh, PA", 185},
			{"Columbus, OH", "Louisville, KY", 210}, {"Columbus, OH", "Detroit, MI", 203},
		},
		lumperPct: 35,
	},
	{
		id: "ostrava-components", short: "oc", name: "Ostrava Components", dc: "Ostrava Plant Greenville",
		carriers: []carrier{{"pellston-transport", "PTX"}, {"harlow-lines", "HRL"}},
		lanes: []laneDef{
			{"Greenville, SC", "Atlanta, GA", 145}, {"Greenville, SC", "Charlotte, NC", 100},
			{"Greenville, SC", "Nashville, TN", 330}, {"Greenville, SC", "Raleigh, NC", 240},
		},
		lumperPct: 5,
	},
}

func fuelBands() []tariff.FuelBand {
	var out []tariff.FuelBand
	for i := 0; i < 16; i++ {
		out = append(out, tariff.FuelBand{FromCPG: int64(300 + 25*i), ToCPG: int64(325 + 25*i), SurchargeBps: int64(1600 + 100*i)})
	}
	return out
}

// TariffFor builds a carrier's tariff for a tenant. Exported for tests.
func TariffFor(td tenantDef, c carrier, ci int) tariff.Tariff {
	t := tariff.Tariff{
		ID: fmt.Sprintf("%s-%s", td.short, c.id), Version: 1, CarrierID: c.id, Currency: "USD",
		Fuel: tariff.FuelSchedule{Bands: fuelBands()},
		Detention: tariff.DetentionRule{FreeMinutes: 120, RatePerHourCents: 7_500, IncrementMinutes: 15,
			CapPerStopCents: 60_000, LateGraceMinutes: 30},
		LiftgateCents: 7_500, StopOffCents: 10_000, LumperCapCents: 35_000,
	}
	for li, l := range td.lanes {
		t.Lanes = append(t.Lanes, tariff.Lane{
			ID: fmt.Sprintf("L%d", li+1), Origin: l.origin, Destination: l.dest,
			RatePerMileCents: tariff.Cents(255 + 12*li + 9*ci), MinimumCents: 45_000,
		})
	}
	return t
}

type gen struct {
	cfg   Config
	r     *rand.Rand
	out   []timed
	truth map[string]Truth
}

type timed struct {
	at  time.Time // when the message reaches the broker
	env events.Envelope
}

func (g *gen) pct(p int) bool       { return g.r.IntN(100) < p }
func (g *gen) between(a, b int) int { return a + g.r.IntN(b-a+1) }
func (g *gen) secs(max int) time.Duration {
	return time.Duration(g.r.IntN(max+1)) * time.Second
}

func (g *gen) emit(at time.Time, env events.Envelope) { g.out = append(g.out, timed{at: at, env: env}) }

// Generate builds a scenario.
func Generate(cfg Config) Scenario {
	if cfg.Start.IsZero() {
		cfg.Start = DefaultConfig().Start
	}
	g := &gen{cfg: cfg, r: rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x7011_6a7e)), truth: map[string]Truth{}}
	sc := Scenario{Config: cfg}
	for _, td := range tenantDefs {
		sc.Tenants = append(sc.Tenants, Tenant{ID: td.id, Name: td.name, Policy: audit.DefaultPolicy()})
		tariffs := map[string]tariff.Tariff{}
		for ci, c := range td.carriers {
			tf := TariffFor(td, c, ci)
			tariffs[c.id] = tf
			at := cfg.Start.Add(-30 * 24 * time.Hour)
			g.emit(at, events.Must("tariff-"+tf.ID+"-v1", td.id, events.TariffPublished, "contracts", "", at, events.TariffPayload{Tariff: tf}))
		}
		for i := 0; i < cfg.ShipmentsPerTenant; i++ {
			c := td.carriers[g.r.IntN(len(td.carriers))]
			g.shipment(td, c, tariffs[c.id], i)
		}
	}
	sc.Truth = g.truth
	sc.Messages = g.deliver()
	return sc
}

type stopTruth struct {
	stop       timeline.Stop
	arr, dep   time.Time
	detention  bool
	dock       bool
	carrierDep time.Time // what the carrier reports and bills from
}

func (g *gen) shipment(td tenantDef, c carrier, tf tariff.Tariff, i int) {
	id := fmt.Sprintf("shp-%s-%04d", td.short, i+1)
	li := g.r.IntN(len(td.lanes))
	lane := td.lanes[li]
	tendered := g.cfg.Start.Add(time.Duration(i*5+g.r.IntN(4)) * time.Hour)
	pickupAppt := tendered.Add(18 * time.Hour).Truncate(time.Hour)
	driveH := lane.miles/50 + 2

	// Decide the injected error first; it shapes the physical events.
	var kind ErrorKind
	if g.pct(g.cfg.ErrorRatePct) {
		kinds := []ErrorKind{DetentionInflated, DetentionLateArrival, DetentionNoEvidence, FuelWrongBand, RateTypo,
			DuplicateInvoice, UncontractedCharge, LiftgateNotRequested, LumperNoReceipt}
		kind = kinds[g.r.IntN(len(kinds))]
	}

	nStops := 2
	if g.pct(20) {
		nStops = 3
	}
	stops := []stopTruth{{stop: timeline.Stop{Seq: 1, Facility: td.dc, Type: "pickup", Appointment: pickupAppt}, dock: true}}
	appt := pickupAppt
	for s := 2; s <= nStops; s++ {
		appt = appt.Add(time.Duration(driveH/(nStops-1)+2) * time.Hour).Truncate(time.Hour)
		name := lane.dest
		if s < nStops {
			name = "Cross-dock en route"
		}
		stops = append(stops, stopTruth{stop: timeline.Stop{Seq: s, Facility: name, Type: "delivery", Appointment: appt}})
	}

	free := tf.Detention.FreeMinutes
	for k := range stops {
		st := &stops[k]
		st.arr = st.stop.Appointment.Add(time.Duration(g.between(-40, 20)) * time.Minute)
		dwell := g.between(45, free-10)
		if g.pct(30) {
			st.detention = true
			dwell = free + 15*g.between(1, 8) + 7 // mid-increment, so sub-minute sensor noise never changes the bill
		}
		st.dep = st.arr.Add(time.Duration(dwell) * time.Minute)
	}
	last := len(stops) - 1

	// Shape the error on the final delivery stop.
	eld := g.pct(90)
	switch kind {
	case DetentionInflated:
		eld = true
	case DetentionLateArrival:
		eld = true
		st := &stops[last]
		st.arr = st.stop.Appointment.Add(time.Duration(g.between(50, 90)) * time.Minute)
		st.dep = st.arr.Add(time.Duration(free+15*g.between(2, 6)+7) * time.Minute)
		st.detention = true
	case DetentionNoEvidence:
		eld = false
		st := &stops[last]
		st.detention = true
		st.dep = maxTime(st.arr, st.stop.Appointment).Add(time.Duration(free+15*g.between(2, 6)+7) * time.Minute)
	}
	for k := range stops {
		if stops[k].detention && !stops[k].dock && kind != DetentionNoEvidence {
			eld = true // clean detention always has independent evidence in this world
		}
		stops[k].carrierDep = stops[k].dep
	}
	if kind == DetentionInflated {
		st := &stops[last]
		start := st.arr
		if start.Before(st.stop.Appointment) {
			start = st.stop.Appointment
		}
		st.carrierDep = maxTime(st.dep.Add(time.Duration(g.between(45, 120))*time.Minute), start.Add(time.Duration(free+37)*time.Minute))
	}

	sh := audit.Shipment{
		TenantID: td.id, ID: id, CarrierID: c.id, TariffID: tf.ID, TariffVersion: tf.Version, LaneID: fmt.Sprintf("L%d", li+1),
		Miles: lane.miles, FuelPriceCPG: int64(g.between(372, 418)), LiftgateRequested: g.pct(15), TenderedAt: tendered,
	}
	if kind == LiftgateNotRequested {
		sh.LiftgateRequested = false
	}
	for _, st := range stops {
		sh.Stops = append(sh.Stops, st.stop)
	}
	g.emit(tendered, events.Must("tms-"+id, td.id, events.ShipmentTendered, "tms", id, tendered, events.ShipmentPayload{Shipment: sh}))

	// Stop events from each source, each with its own reporting delay.
	invoiceAt := stops[last].dep.Add(time.Duration(g.between(2, 9)*24) * time.Hour).Truncate(time.Hour)
	lateELD := g.pct(g.cfg.LateTelematicsPct)
	for _, st := range stops {
		seq := st.stop.Seq
		stop := func(src timeline.Source, kind timeline.Kind, n int, at, arrives time.Time) {
			eid := fmt.Sprintf("%s-%s-%d-%s-%d", src, id, seq, kind, n)
			g.emit(arrives, events.Must(eid, td.id, events.StopReported, string(src), id, at,
				events.StopPayload{StopSeq: seq, Kind: kind, At: at}))
		}
		if st.dock {
			stop(timeline.DockSystem, timeline.Arrived, 1, st.arr, st.arr.Add(g.secs(90)))
			stop(timeline.DockSystem, timeline.Departed, 1, st.dep, st.dep.Add(g.secs(90)))
		}
		if eld {
			delay := func() time.Duration { return time.Duration(g.between(1, 30)) * time.Minute }
			if lateELD {
				delay = func() time.Duration { return invoiceAt.Sub(st.dep) + time.Duration(g.between(6, 72))*time.Hour }
			}
			a, d := st.arr.Add(g.secs(40)), st.dep.Add(g.secs(40))
			stop(timeline.ELDGeofence, timeline.Arrived, 1, a, a.Add(delay()))
			if g.pct(30) { // yard move: re-entering the geofence later must not move the arrival
				x := a.Add(time.Duration(g.between(5, 20)) * time.Minute)
				stop(timeline.ELDGeofence, timeline.Arrived, 2, x, x.Add(delay()))
				y := a.Add(time.Duration(g.between(1, 4)) * time.Minute)
				stop(timeline.ELDGeofence, timeline.Departed, 2, y, y.Add(delay()))
			}
			stop(timeline.ELDGeofence, timeline.Departed, 1, d, d.Add(delay()))
		}
		edi := time.Duration(g.between(5, 90)) * time.Minute
		stop(timeline.CarrierEDI, timeline.Arrived, 1, st.arr.Truncate(time.Minute), st.arr.Add(edi))
		stop(timeline.CarrierEDI, timeline.Departed, 1, st.carrierDep.Truncate(time.Minute), st.carrierDep.Add(edi))
	}

	// The carrier's invoice, computed from the carrier's own view of events.
	l, _ := tf.Lane(sh.LaneID)
	linehaul := tariff.LinehaulCents(l, sh.Miles)
	if kind == RateTypo {
		linehaul += linehaul * tariff.Cents(g.between(8, 15)) / 100
	}
	fuel, band, _ := tariff.FuelCents(tf.Fuel, linehaul, sh.FuelPriceCPG)
	if kind == FuelWrongBand {
		fuel = tariff.Cents((int64(linehaul)*(band.SurchargeBps+100) + 5_000) / 10_000)
	}
	lines := []audit.InvoiceLine{{Code: tariff.Linehaul, AmountCents: linehaul}, {Code: tariff.Fuel, AmountCents: fuel}}
	for _, st := range stops {
		if !st.detention {
			continue
		}
		rule := tf.Detention
		if kind == DetentionLateArrival {
			rule.LateGraceMinutes = 1 << 20 // the carrier ignores the late-arrival clause
		}
		if amt := tariff.DetentionCents(rule, st.stop.Appointment, st.arr.Truncate(time.Minute), st.carrierDep.Truncate(time.Minute)).AmountCents; amt > 0 {
			lines = append(lines, audit.InvoiceLine{Code: tariff.Detention, StopSeq: st.stop.Seq, AmountCents: amt})
		}
	}
	if kind == DetentionInflated && !stops[last].detention {
		st := stops[last]
		amt := tariff.DetentionCents(tf.Detention, st.stop.Appointment, st.arr.Truncate(time.Minute), st.carrierDep.Truncate(time.Minute)).AmountCents
		lines = append(lines, audit.InvoiceLine{Code: tariff.Detention, StopSeq: st.stop.Seq, AmountCents: amt})
	}
	if nStops > 2 {
		lines = append(lines, audit.InvoiceLine{Code: tariff.StopOff, AmountCents: tf.StopOffCents * tariff.Cents(nStops-2)})
	}
	if sh.LiftgateRequested || kind == LiftgateNotRequested {
		lines = append(lines, audit.InvoiceLine{Code: tariff.Liftgate, StopSeq: last + 1, AmountCents: tf.LiftgateCents})
	}
	if g.pct(td.lumperPct) || kind == LumperNoReceipt {
		ln := audit.InvoiceLine{Code: tariff.Lumper, StopSeq: last + 1, AmountCents: tariff.Cents(g.between(180, 300) * 100)}
		if kind != LumperNoReceipt {
			ln.ReceiptRef = fmt.Sprintf("LR-%06d", g.r.IntN(1_000_000))
		}
		lines = append(lines, ln)
	}
	if kind == UncontractedCharge {
		lines = append(lines, audit.InvoiceLine{Code: "INSIDE_DELIVERY", StopSeq: last + 1, AmountCents: 9_500})
	}

	number := fmt.Sprintf("%s-%06d", c.prefix, g.r.IntN(1_000_000))
	invID := fmt.Sprintf("inv-%s-%04d", td.short, i+1)
	inv := audit.Invoice{TenantID: td.id, ID: invID, Number: number, Revision: 1, CarrierID: c.id, ShipmentID: id, Lines: lines, ReceivedAt: invoiceAt}
	g.emit(invoiceAt, events.Must("edi210-"+invID+"-r1", td.id, events.InvoiceReceived, "carrier_billing", id, invoiceAt, events.InvoicePayload{Invoice: inv}))
	t := Truth{TenantID: td.id, ShipmentID: id, InvoiceID: invID}
	if kind != "" && kind != DuplicateInvoice {
		t.Errors = []ErrorKind{kind}
	}
	g.truth[invID] = t

	if kind == DuplicateInvoice {
		dup := inv
		dup.ID = invID + "-d"
		dup.ReceivedAt = invoiceAt.Add(time.Duration(g.between(7, 20)*24) * time.Hour)
		g.emit(dup.ReceivedAt, events.Must("edi210-"+dup.ID+"-r1", td.id, events.InvoiceReceived, "carrier_billing", id, dup.ReceivedAt, events.InvoicePayload{Invoice: dup}))
		g.truth[dup.ID] = Truth{TenantID: td.id, ShipmentID: id, InvoiceID: dup.ID, Errors: []ErrorKind{DuplicateInvoice}}
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// deliver orders messages as a broker would hand them out, adds redeliveries and
// assigns partitions and offsets.
func (g *gen) deliver() []Message {
	slices.SortStableFunc(g.out, func(a, b timed) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		if a.env.EventID < b.env.EventID {
			return -1
		}
		return 1
	})
	envs := make([]events.Envelope, len(g.out))
	for i, t := range g.out {
		envs[i] = t.env
	}
	if g.cfg.Shuffled {
		g.r.Shuffle(len(envs), func(i, j int) { envs[i], envs[j] = envs[j], envs[i] })
	}
	type item struct {
		env events.Envelope
		dup bool
	}
	items := make([]item, 0, len(envs))
	for _, e := range envs {
		items = append(items, item{env: e})
	}
	// Redeliver a share of messages a little later in the stream (consumer crash before commit).
	n := len(items)
	for i := 0; i < n; i++ {
		if g.pct(g.cfg.DuplicatePct) {
			at := min(len(items), i+1+g.r.IntN(12))
			items = slices.Insert(items, at, item{env: items[i].env, dup: true})
		}
	}
	offsets := make([]int64, Partitions)
	out := make([]Message, 0, len(items))
	for _, it := range items {
		p := PartitionFor(it.env.Key())
		out = append(out, Message{Partition: p, Offset: offsets[p], Redelivery: it.dup, Envelope: it.env})
		offsets[p]++
	}
	return out
}

// PartitionFor mirrors a hash partitioner for display; the real producer uses the
// client's own Kafka-compatible partitioner.
func PartitionFor(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % Partitions)
}
