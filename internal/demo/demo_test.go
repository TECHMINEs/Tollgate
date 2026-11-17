package demo

import (
	"testing"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/sim"
)

func TestSessionDashboard(t *testing.T) {
	s := NewSession(sim.DefaultConfig(), nil)
	for !s.Done() {
		s.Step(100)
	}
	for _, tn := range s.Meta().Tenants {
		d, err := s.Dashboard(tn.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		if d.KPIs.Invoices == 0 || d.KPIs.Held != 0 {
			t.Fatalf("%s: %+v", tn.ID, d.KPIs)
		}
		if d.Detection.CleanFlagged != 0 || d.Detection.Caught != d.Detection.Injected {
			t.Fatalf("%s detection: %+v", tn.ID, d.Detection)
		}
		if d.KPIs.Approved+d.KPIs.Disputed != d.KPIs.Billed {
			t.Fatalf("%s: approved + disputed should equal billed: %+v", tn.ID, d.KPIs)
		}
		sd, err := s.Shipment(tn.ID, d.Invoices[0].ShipmentID)
		if err != nil || len(sd.Timeline.Stops) == 0 || len(sd.Verdicts) == 0 {
			t.Fatalf("shipment detail: %v %+v", err, sd)
		}
	}
}

func TestReplayFingerprintsMatch(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.ShipmentsPerTenant = 15
	runs := ReplayCheck(cfg)
	for _, r := range runs[1:] {
		if r.Fingerprint != runs[0].Fingerprint {
			t.Fatalf("%s fingerprint %s != %s", r.Name, r.Fingerprint, runs[0].Fingerprint)
		}
	}
	if runs[3].Dropped == 0 {
		t.Fatal("redelivery run should drop duplicates")
	}
}

func TestWhatIfDisputeCost(t *testing.T) {
	cfg := sim.DefaultConfig()
	tenant := "larchmont-grocers"
	cheap, expensive := audit.DefaultPolicy(), audit.DefaultPolicy()
	cheap.DisputeCostCents, expensive.DisputeCostCents = 0, 50_000
	a, _ := WhatIf(cfg, tenant, cheap)
	b, _ := WhatIf(cfg, tenant, expensive)
	if a.KPIs.Disputed <= b.KPIs.Disputed || a.KPIs.Leakage >= b.KPIs.Leakage {
		t.Fatalf("a costlier dispute should mean fewer disputes and more accepted leakage: %+v vs %+v", a.KPIs, b.KPIs)
	}
}
