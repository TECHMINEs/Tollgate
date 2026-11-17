package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/pipeline"
	"github.com/TECHMINEs/Tollgate/internal/sim"
)

func run(t *testing.T, sc sim.Scenario) *pipeline.Memory {
	t.Helper()
	mem := pipeline.NewMemory()
	for _, tn := range sc.Tenants {
		mem.AddTenant(tn.ID, tn.Name, tn.Policy)
	}
	p := &pipeline.Processor{Store: mem}
	for _, m := range sc.Messages {
		res, err := p.Handle(context.Background(), m.Envelope)
		if err != nil {
			t.Fatalf("offset %d/%d %s: %v", m.Partition, m.Offset, m.Envelope.EventID, err)
		}
		if res.Duplicate != m.Redelivery && !m.Redelivery {
			t.Fatalf("%s: first delivery reported as duplicate", m.Envelope.EventID)
		}
	}
	return mem
}

// final is the order-independent outcome: latest verdict per invoice and ledger balances.
type final struct {
	Verdicts map[string]audit.Verdict
	Balances map[string]map[ledger.Account]ledger.Cents
}

func outcome(t *testing.T, mem *pipeline.Memory, sc sim.Scenario) final {
	f := final{Verdicts: map[string]audit.Verdict{}, Balances: map[string]map[ledger.Account]ledger.Cents{}}
	for _, tn := range sc.Tenants {
		s, err := mem.Snapshot(tn.ID)
		if err != nil {
			t.Fatal(err)
		}
		for id, vs := range s.Verdicts {
			v := vs[len(vs)-1]
			v.Version = 0 // how many versions it took depends on arrival order; the outcome must not
			f.Verdicts[id] = v
		}
		f.Balances[tn.ID] = ledger.Balances(s.Postings)
	}
	return f
}

func TestAnswerKey(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.ShipmentsPerTenant = 60
	sc := sim.Generate(cfg)
	f := outcome(t, run(t, sc), sc)

	if len(f.Verdicts) != len(sc.Truth) {
		t.Fatalf("audited %d invoices, generated %d", len(f.Verdicts), len(sc.Truth))
	}
	injected, caught := 0, 0
	for id, truth := range sc.Truth {
		v := f.Verdicts[id]
		if len(truth.Errors) == 0 {
			if v.Decision != audit.Approve {
				t.Errorf("clean invoice %s flagged %s: %+v", id, v.Decision, v.Lines)
			}
			continue
		}
		injected++
		if v.Decision == audit.Approve || v.Decision == audit.Hold {
			t.Errorf("%s with %v slipped through as %s", id, truth.Errors, v.Decision)
			continue
		}
		caught++
	}
	if injected == 0 {
		t.Fatal("scenario injected no errors")
	}
	t.Logf("%d invoices, %d with injected errors, %d caught", len(sc.Truth), injected, caught)
}

func TestOrderAndRedeliveryDoNotChangeTheOutcome(t *testing.T) {
	cfg := sim.DefaultConfig()
	cfg.DuplicatePct = 0
	want := outcome(t, run(t, sim.Generate(cfg)), sim.Generate(cfg))

	for _, variant := range []struct {
		name string
		mod  func(*sim.Config)
	}{
		{"shuffled", func(c *sim.Config) { c.Shuffled = true }},
		{"redelivered", func(c *sim.Config) { c.DuplicatePct = 40 }},
		{"shuffled and redelivered", func(c *sim.Config) { c.Shuffled = true; c.DuplicatePct = 40 }},
	} {
		c := cfg
		variant.mod(&c)
		sc := sim.Generate(c)
		got := outcome(t, run(t, sc), sc)
		if !reflect.DeepEqual(got.Balances, want.Balances) {
			t.Errorf("%s: ledger balances differ\n got %v\nwant %v", variant.name, got.Balances, want.Balances)
		}
		for id, w := range want.Verdicts {
			if g := got.Verdicts[id]; !reflect.DeepEqual(g, w) {
				t.Errorf("%s: verdict %s differs: got %s %s, want %s %s", variant.name, id, g.Decision, g.EvidenceHash[:8], w.Decision, w.EvidenceHash[:8])
			}
		}
	}
}

func TestReplayIsANoOp(t *testing.T) {
	sc := sim.Generate(sim.DefaultConfig())
	mem := run(t, sc)
	before, _ := mem.Snapshot(sc.Tenants[0].ID)
	p := &pipeline.Processor{Store: mem}
	for _, m := range sc.Messages {
		res, err := p.Handle(context.Background(), m.Envelope)
		if err != nil || !res.Duplicate || len(res.Postings) > 0 {
			t.Fatalf("replay of %s: dup=%v postings=%d err=%v", m.Envelope.EventID, res.Duplicate, len(res.Postings), err)
		}
	}
	after, _ := mem.Snapshot(sc.Tenants[0].ID)
	if len(after.Postings) != len(before.Postings) || after.Outbox != before.Outbox {
		t.Fatal("replay changed the ledger or outbox")
	}
}

func TestLedgerAlwaysBalances(t *testing.T) {
	sc := sim.Generate(sim.Config{Seed: 99, ShipmentsPerTenant: 40, ErrorRatePct: 50, DuplicatePct: 10, LateTelematicsPct: 40, Shuffled: true})
	mem := run(t, sc)
	for _, tn := range sc.Tenants {
		s, _ := mem.Snapshot(tn.ID)
		var sum ledger.Cents
		for _, b := range ledger.Balances(s.Postings) {
			sum += b
		}
		if sum != 0 {
			t.Fatalf("%s trial balance is off by %v", tn.ID, sum)
		}
	}
}

func TestTenantIsolationAndPermanentErrors(t *testing.T) {
	sc := sim.Generate(sim.DefaultConfig())
	mem := run(t, sc)
	p := &pipeline.Processor{Store: mem}
	ctx := context.Background()

	// A tenant that was never provisioned is dead-lettered, not retried.
	env := sc.Messages[len(sc.Messages)-1].Envelope
	env.TenantID, env.EventID = "unknown-co", "x-1"
	if _, err := p.Handle(ctx, env); !pipeline.IsPermanent(err) {
		t.Fatalf("unknown tenant: %v", err)
	}

	// Tenant B's view contains none of tenant A's shipments.
	a, _ := mem.Snapshot(sc.Tenants[0].ID)
	b, _ := mem.Snapshot(sc.Tenants[1].ID)
	ids := map[string]bool{}
	for _, s := range a.Shipments {
		ids[s.ID] = true
	}
	for _, s := range b.Shipments {
		if ids[s.ID] || s.TenantID != sc.Tenants[1].ID {
			t.Fatalf("shipment %s leaked across tenants", s.ID)
		}
	}

	// A payload that claims another tenant is rejected.
	var tender events.Envelope
	for _, m := range sc.Messages {
		if m.Envelope.Type == events.ShipmentTendered && m.Envelope.TenantID == sc.Tenants[0].ID {
			tender = m.Envelope
			break
		}
	}
	forged := tender
	forged.TenantID, forged.EventID = sc.Tenants[1].ID, "forged-1"
	if _, err := p.Handle(ctx, forged); !pipeline.IsPermanent(err) {
		t.Fatalf("cross-tenant payload should be permanent, got %v", err)
	}
}

func TestFailedMessageLeavesNoTrace(t *testing.T) {
	sc := sim.Generate(sim.DefaultConfig())
	mem := run(t, sc)
	p := &pipeline.Processor{Store: mem}
	ctx := context.Background()

	var orig events.Envelope
	for _, m := range sc.Messages {
		if m.Envelope.Type == events.TariffPublished {
			orig = m.Envelope
			break
		}
	}
	var tp events.TariffPayload
	_ = json.Unmarshal(orig.Payload, &tp)
	tp.Tariff.LiftgateCents++ // same version, different terms
	changed := events.Must("tariff-edit-1", orig.TenantID, orig.Type, orig.Source, "", orig.OccurredAt, tp)
	_, err := p.Handle(ctx, changed)
	var perm pipeline.PermanentError
	if !errors.As(err, &perm) {
		t.Fatalf("editing a published tariff version should be permanent, got %v", err)
	}
	// The inbox claim was rolled back with the rest of the transaction.
	tp.Tariff.LiftgateCents--
	same := events.Must("tariff-edit-1", orig.TenantID, orig.Type, orig.Source, "", orig.OccurredAt, tp)
	res, err := p.Handle(ctx, same)
	if err != nil || res.Duplicate {
		t.Fatalf("event id should be claimable after rollback: dup=%v err=%v", res.Duplicate, err)
	}
}

func Example() {
	sc := sim.Generate(sim.DefaultConfig())
	mem := pipeline.NewMemory()
	for _, tn := range sc.Tenants {
		mem.AddTenant(tn.ID, tn.Name, tn.Policy)
	}
	p := &pipeline.Processor{Store: mem}
	dups := 0
	for _, m := range sc.Messages {
		if res, _ := p.Handle(context.Background(), m.Envelope); res.Duplicate {
			dups++
		}
	}
	fmt.Println(dups > 0)
	// Output: true
}

func TestAnswerKeyAcrossSeeds(t *testing.T) {
	for seed := uint64(1); seed <= 25; seed++ {
		cfg := sim.DefaultConfig()
		cfg.Seed, cfg.Shuffled = seed, seed%2 == 0
		sc := sim.Generate(cfg)
		f := outcome(t, run(t, sc), sc)
		for id, truth := range sc.Truth {
			v := f.Verdicts[id]
			clean := len(truth.Errors) == 0
			if clean != (v.Decision == audit.Approve) {
				t.Errorf("seed %d %s errors=%v decision=%s reasons=%v", seed, id, truth.Errors, v.Decision, v.Reasons)
			}
		}
	}
}
