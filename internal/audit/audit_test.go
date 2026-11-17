package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

var t0 = time.Date(2026, 10, 6, 8, 0, 0, 0, time.UTC)

func m(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func band(from, to, bps int64) tariff.FuelBand {
	return tariff.FuelBand{FromCPG: from, ToCPG: to, SurchargeBps: bps}
}

func ev(id string, src timeline.Source, seq int, kind timeline.Kind, at time.Time) timeline.Event {
	return timeline.Event{EventID: id, Source: src, StopSeq: seq, Kind: kind, At: at}
}

func testTariff() *tariff.Tariff {
	return &tariff.Tariff{ID: "tf", Version: 1, CarrierID: "acme", Currency: "USD",
		Lanes: []tariff.Lane{{ID: "L1", RatePerMileCents: 250, MinimumCents: 45_000}},
		Fuel:  tariff.FuelSchedule{Bands: []tariff.FuelBand{band(300, 400, 2000), band(400, 500, 2100)}},
		Detention: tariff.DetentionRule{FreeMinutes: 120, RatePerHourCents: 7_500, IncrementMinutes: 15,
			CapPerStopCents: 60_000, LateGraceMinutes: 30},
		LiftgateCents: 7_500, StopOffCents: 10_000, LumperCapCents: 35_000}
}

func testShipment() Shipment {
	return Shipment{TenantID: "t1", ID: "s1", CarrierID: "acme", TariffID: "tf", TariffVersion: 1, LaneID: "L1",
		Miles: 200, FuelPriceCPG: 380,
		Stops: []timeline.Stop{{Seq: 1, Type: "pickup", Appointment: m(0)}, {Seq: 2, Type: "delivery", Appointment: m(480)}}}
}

// Linehaul 200 mi x $2.50 = $500; fuel 20% = $100.
func cleanLines() []InvoiceLine {
	return []InvoiceLine{{Code: tariff.Linehaul, AmountCents: 50_000}, {Code: tariff.Fuel, AmountCents: 10_000}}
}

func evidence(src2 timeline.Source, arr2, dep2 int) timeline.Timeline {
	evs := []timeline.Event{
		ev("d1a", timeline.DockSystem, 1, timeline.Arrived, m(-10)), ev("d1d", timeline.DockSystem, 1, timeline.Departed, m(60)),
		ev("x2a", src2, 2, timeline.Arrived, m(arr2)), ev("x2d", src2, 2, timeline.Departed, m(dep2)),
	}
	return timeline.Build(testShipment().Stops, evs, 10*time.Minute)
}

func input(lines ...InvoiceLine) Input {
	return Input{Shipment: testShipment(), Tariff: testTariff(), Timeline: evidence(timeline.ELDGeofence, 475, 560),
		Invoice: Invoice{TenantID: "t1", ID: "i1", Number: "A-1", Revision: 1, CarrierID: "acme", ShipmentID: "s1",
			Lines: append(cleanLines(), lines...), ReceivedAt: m(5000)},
		Policy: DefaultPolicy()}
}

func TestCleanInvoiceIsApproved(t *testing.T) {
	v := Audit(input())
	if v.Decision != Approve || v.ApprovedCents != 60_000 || v.DisputedCents != 0 {
		t.Fatalf("got %s approved=%v disputed=%v: %+v", v.Decision, v.ApprovedCents, v.DisputedCents, v.Lines)
	}
}

func TestInflatedDetentionIsShortPaid(t *testing.T) {
	// ELD: arrived 475 (early, clock starts at 480), left 690 -> 210 min dwell, 90 billable = 6 x 15 min = $112.50.
	in := input(InvoiceLine{Code: tariff.Detention, StopSeq: 2, AmountCents: 22_500}) // carrier billed 12 increments
	in.Timeline = evidence(timeline.ELDGeofence, 475, 690)
	v := Audit(in)
	l := v.Lines[2]
	if l.Finding != Overbilled || l.ExpectedCents != 11_250 || l.VarianceCents != 11_250 {
		t.Fatalf("detention line: %+v", l)
	}
	if v.Decision != ShortPay || v.ApprovedCents != 60_000+11_250 || v.DisputedCents != 11_250 {
		t.Fatalf("verdict: %s approved=%v disputed=%v", v.Decision, v.ApprovedCents, v.DisputedCents)
	}
	if l.Detention == nil || l.Detention.DwellMinutes != 210 {
		t.Fatalf("finding should carry its working: %+v", l.Detention)
	}
}

func TestLateArrivalForfeitsDetention(t *testing.T) {
	in := input(InvoiceLine{Code: tariff.Detention, StopSeq: 2, AmountCents: 7_500})
	in.Timeline = evidence(timeline.ELDGeofence, 540, 800) // 60 min late
	l := Audit(in).Lines[2]
	if l.Finding != Overbilled || l.ExpectedCents != 0 || !strings.Contains(l.Reason, "forfeits") {
		t.Fatalf("got %+v", l)
	}
}

func TestCarrierOnlyDetentionIsUnsupported(t *testing.T) {
	in := input(InvoiceLine{Code: tariff.Detention, StopSeq: 2, AmountCents: 15_000}) // 240 min dwell, 8 increments
	in.Timeline = evidence(timeline.CarrierEDI, 480, 720)
	if l := Audit(in).Lines[2]; l.Finding != Unsupported {
		t.Fatalf("got %+v", l)
	}
	in.Policy.RequireIndependentForDet = false
	if l := Audit(in).Lines[2]; l.Finding != Match {
		t.Fatalf("with the policy off, carrier times are accepted: %+v", l)
	}
}

func TestMissingEvidenceHolds(t *testing.T) {
	in := input(InvoiceLine{Code: tariff.Detention, StopSeq: 2, AmountCents: 7_500})
	in.Timeline = timeline.Build(in.Shipment.Stops, nil, time.Minute)
	v := Audit(in)
	if v.Decision != Hold || v.ApprovedCents != 0 {
		t.Fatalf("got %s", v.Decision)
	}
	in = input()
	in.Tariff = nil
	if v := Audit(in); v.Decision != Hold {
		t.Fatalf("no tariff should hold, got %s", v.Decision)
	}
}

func TestSmallVarianceIsNotWorthDisputing(t *testing.T) {
	in := input()
	in.Invoice.Lines[1].AmountCents = 12_000 // $20 over on fuel; 90% x $20 < $35 dispute cost
	v := Audit(in)
	if v.Decision != ApproveVariance || v.LeakageCents != 2_000 || v.ApprovedCents != 62_000 {
		t.Fatalf("got %s leakage=%v", v.Decision, v.LeakageCents)
	}
}

func TestUncontractedLiftgateLumper(t *testing.T) {
	v := Audit(input(
		InvoiceLine{Code: "INSIDE_DELIVERY", AmountCents: 9_500},
		InvoiceLine{Code: tariff.Liftgate, AmountCents: 7_500},
		InvoiceLine{Code: tariff.Lumper, AmountCents: 20_000},
		InvoiceLine{Code: tariff.Lumper, AmountCents: 40_000, ReceiptRef: "R1"},
	))
	want := []Finding{Match, Match, Uncontracted, Unsupported, Unsupported, Overbilled}
	for i, f := range want {
		if v.Lines[i].Finding != f {
			t.Errorf("line %d (%s): got %s, want %s", i+1, v.Lines[i].Code, v.Lines[i].Finding, f)
		}
	}
	if v.Lines[5].ExpectedCents != 35_000 {
		t.Errorf("lumper should be capped at $350, got %v", v.Lines[5].ExpectedCents)
	}
}

func TestDuplicates(t *testing.T) {
	first := input().Invoice
	dup := first
	dup.ID, dup.ReceivedAt = "i2", first.ReceivedAt.Add(time.Hour)
	in := input()
	in.Invoice, in.Others = dup, []Invoice{first}
	if v := Audit(in); v.Decision != Reject || v.ApprovedCents != 0 || v.DisputedCents != 60_000 {
		t.Fatalf("re-sent invoice number: %s", v.Decision)
	}
	// The original is unaffected by the duplicate's existence.
	in.Invoice, in.Others = first, []Invoice{dup}
	if v := Audit(in); v.Decision != Approve {
		t.Fatalf("original should still approve, got %s", v.Decision)
	}
	// A new invoice number re-billing the linehaul is a line-level duplicate.
	rebill := dup
	rebill.Number = "A-2"
	rebill.Lines = []InvoiceLine{{Code: tariff.Linehaul, AmountCents: 50_000}}
	in.Invoice, in.Others = rebill, []Invoice{first}
	if v := Audit(in); v.Lines[0].Finding != Duplicate || v.Decision != ShortPay {
		t.Fatalf("re-billed linehaul: %s %+v", v.Decision, v.Lines[0])
	}
}

func TestEvidenceHash(t *testing.T) {
	a, b := Audit(input()), Audit(input())
	if a.EvidenceHash != b.EvidenceHash {
		t.Fatal("same inputs must hash the same")
	}
	in := input()
	in.Timeline = evidence(timeline.ELDGeofence, 475, 690)
	in.Timeline.EventIDs = append(in.Timeline.EventIDs, "late-event")
	if Audit(in).EvidenceHash == a.EvidenceHash {
		t.Fatal("new evidence must change the hash")
	}
}

func TestExpectedCost(t *testing.T) {
	in := input()
	in.Timeline = evidence(timeline.ELDGeofence, 475, 690)
	got, ok := ExpectedCost(in.Shipment, in.Tariff, in.Timeline, in.Policy)
	if !ok || got != 60_000+11_250 {
		t.Fatalf("got %v %v", got, ok)
	}
}
