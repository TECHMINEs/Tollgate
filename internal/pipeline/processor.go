// Package pipeline applies inbound freight documents and re-audits the affected shipment,
// inside one tenant-scoped transaction per message.
//
// Delivery is at-least-once. Exactly-once *effects* come from three things committed in
// the same transaction: the inbox claim (drops redelivered messages), the evidence hash
// on verdicts (drops re-audits that would not change anything), and the transactional
// outbox (downstream messages are published only if the transaction commits).
package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

// ErrUnknownTenant is returned by stores for a tenant that has not been provisioned.
var ErrUnknownTenant = errors.New("unknown tenant")

// ErrNotFound is returned by store lookups that find nothing.
var ErrNotFound = errors.New("not found")

// Store opens tenant-scoped transactions. Every read and write inside fn sees only the
// named tenant's rows.
type Store interface {
	InTenant(ctx context.Context, tenantID string, fn func(Tx) error) error
}

// Tx is the unit of work the processor needs. Implementations: Postgres (row-level
// security) and Memory (tests and the browser demo).
type Tx interface {
	Policy(ctx context.Context) (audit.Policy, error)
	// ClaimEvent records an inbound event ID; false means it was already processed.
	ClaimEvent(ctx context.Context, source, eventID string) (bool, error)
	// LockShipment serialises work on one shipment for the rest of the transaction.
	LockShipment(ctx context.Context, shipmentID string) error

	PutTariff(ctx context.Context, t tariff.Tariff) (existing *tariff.Tariff, err error)
	Tariff(ctx context.Context, id string, version int) (*tariff.Tariff, error)
	ShipmentsOnTariff(ctx context.Context, id string, version int) ([]string, error)

	PutShipment(ctx context.Context, s audit.Shipment) (existing *audit.Shipment, err error)
	Shipment(ctx context.Context, id string) (*audit.Shipment, error)

	AddStopEvent(ctx context.Context, shipmentID string, e timeline.Event) error
	StopEvents(ctx context.Context, shipmentID string) ([]timeline.Event, error)

	PutInvoice(ctx context.Context, inv audit.Invoice) (existing *audit.Invoice, err error)
	// Invoices returns the latest revision of every invoice for a shipment.
	Invoices(ctx context.Context, shipmentID string) ([]audit.Invoice, error)

	LatestVerdict(ctx context.Context, invoiceID string) (*audit.Verdict, error)
	SaveVerdict(ctx context.Context, v audit.Verdict) error

	Journal(ctx context.Context, key string) (version int, lines []ledger.Line, err error)
	Post(ctx context.Context, key string, version int, lines []ledger.Line, postings []ledger.Posting) error

	Enqueue(ctx context.Context, m OutboxMessage) error
}

// OutboxMessage is a downstream message written in the same transaction as its cause.
type OutboxMessage struct {
	Topic   string          `json:"topic"`
	Key     string          `json:"key"`
	EventID string          `json:"event_id"`
	Payload json.RawMessage `json:"payload"`
}

// PermanentError marks a message that will never succeed; the consumer dead-letters it
// instead of retrying.
type PermanentError struct{ Err error }

func (e PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e PermanentError) Unwrap() error { return e.Err }

func permanent(format string, a ...any) error { return PermanentError{fmt.Errorf(format, a...)} }

// IsPermanent reports whether err should be dead-lettered.
func IsPermanent(err error) bool {
	var p PermanentError
	return errors.As(err, &p) || errors.Is(err, ErrUnknownTenant)
}

// Result describes what one message changed. The demo UI renders it.
type Result struct {
	Duplicate bool             `json:"duplicate"`
	Verdicts  []audit.Verdict  `json:"verdicts,omitempty"`
	Postings  []ledger.Posting `json:"postings,omitempty"`
	Outbox    []OutboxMessage  `json:"outbox,omitempty"`
}

// Processor handles inbound envelopes.
type Processor struct {
	Store Store
}

// Handle applies one envelope. It is safe to call any number of times with the same
// envelope, in any order relative to other envelopes.
func (p *Processor) Handle(ctx context.Context, env events.Envelope) (Result, error) {
	if err := env.Validate(); err != nil {
		return Result{}, PermanentError{err}
	}
	var res Result
	err := p.Store.InTenant(ctx, env.TenantID, func(tx Tx) error {
		res = Result{}
		pol, err := tx.Policy(ctx)
		if err != nil {
			return err
		}
		fresh, err := tx.ClaimEvent(ctx, env.Source, env.EventID)
		if err != nil {
			return err
		}
		if !fresh {
			res.Duplicate = true
			return nil
		}
		w := &work{ctx: ctx, tx: tx, tenant: env.TenantID, pol: pol, at: env.OccurredAt, res: &res}
		return w.apply(env)
	})
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

type work struct {
	ctx    context.Context
	tx     Tx
	tenant string
	pol    audit.Policy
	at     time.Time // event time of the inbound message, stamped on outbound ones
	res    *Result
}

func (w *work) apply(env events.Envelope) error {
	ctx, tx := w.ctx, w.tx
	switch env.Type {
	case events.TariffPublished:
		var p events.TariffPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return permanent("tariff payload: %w", err)
		}
		if err := p.Tariff.Validate(); err != nil {
			return permanent("tariff %s v%d: %w", p.Tariff.ID, p.Tariff.Version, err)
		}
		existing, err := tx.PutTariff(ctx, p.Tariff)
		if err != nil {
			return err
		}
		if existing != nil && !sameJSON(*existing, p.Tariff) {
			return permanent("tariff %s v%d already exists with different terms; publish a new version", p.Tariff.ID, p.Tariff.Version)
		}
		ids, err := tx.ShipmentsOnTariff(ctx, p.Tariff.ID, p.Tariff.Version)
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := w.reaudit(id); err != nil {
				return err
			}
		}
		return nil

	case events.ShipmentTendered:
		var p events.ShipmentPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return permanent("shipment payload: %w", err)
		}
		s := p.Shipment
		if s.ID != env.ShipmentID || (s.TenantID != "" && s.TenantID != w.tenant) {
			return permanent("shipment payload does not match envelope")
		}
		s.TenantID = w.tenant
		if err := tx.LockShipment(ctx, s.ID); err != nil {
			return err
		}
		existing, err := tx.PutShipment(ctx, s)
		if err != nil {
			return err
		}
		if existing != nil && !sameJSON(normShipment(*existing), normShipment(s)) {
			return permanent("shipment %s was already tendered with different details", s.ID)
		}
		return w.reaudit(s.ID)

	case events.StopReported:
		var p events.StopPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return permanent("stop payload: %w", err)
		}
		if err := tx.LockShipment(ctx, env.ShipmentID); err != nil {
			return err
		}
		ev := timeline.Event{EventID: env.EventID, Source: timeline.Source(env.Source), StopSeq: p.StopSeq, Kind: p.Kind, At: p.At.UTC()}
		if err := tx.AddStopEvent(ctx, env.ShipmentID, ev); err != nil {
			return err
		}
		return w.reaudit(env.ShipmentID)

	case events.InvoiceReceived:
		var p events.InvoicePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return permanent("invoice payload: %w", err)
		}
		inv := p.Invoice
		if inv.ShipmentID != env.ShipmentID || (inv.TenantID != "" && inv.TenantID != w.tenant) {
			return permanent("invoice payload does not match envelope")
		}
		if inv.ID == "" || inv.Revision < 1 {
			return permanent("invoice needs id and revision >= 1")
		}
		inv.TenantID = w.tenant
		inv.ReceivedAt = inv.ReceivedAt.UTC()
		if err := tx.LockShipment(ctx, inv.ShipmentID); err != nil {
			return err
		}
		existing, err := tx.PutInvoice(ctx, inv)
		if err != nil {
			return err
		}
		if existing != nil && !sameJSON(*existing, inv) {
			return permanent("invoice %s revision %d already received with different content", inv.ID, inv.Revision)
		}
		return w.reaudit(inv.ShipmentID)
	}
	return permanent("unhandled type %s", env.Type)
}

// sameJSON compares two documents by their canonical JSON encoding.
func sameJSON(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

func normShipment(s audit.Shipment) audit.Shipment {
	s.TenderedAt = s.TenderedAt.UTC()
	for i := range s.Stops {
		s.Stops[i].Appointment = s.Stops[i].Appointment.UTC()
	}
	return s
}

// reaudit recomputes every verdict and the accrual for one shipment from stored
// evidence. Anything whose inputs did not change is left alone.
func (w *work) reaudit(shipmentID string) error {
	ctx, tx := w.ctx, w.tx
	sh, err := tx.Shipment(ctx, shipmentID)
	if errors.Is(err, ErrNotFound) {
		return nil // evidence is stored; the audit runs when the tender arrives
	} else if err != nil {
		return err
	}
	tf, err := tx.Tariff(ctx, sh.TariffID, sh.TariffVersion)
	if errors.Is(err, ErrNotFound) {
		tf = nil
	} else if err != nil {
		return err
	}
	evs, err := tx.StopEvents(ctx, shipmentID)
	if err != nil {
		return err
	}
	tl := timeline.Build(sh.Stops, evs, time.Duration(w.pol.ConflictToleranceMinutes)*time.Minute)
	invs, err := tx.Invoices(ctx, shipmentID)
	if err != nil {
		return err
	}
	audit.SortInvoices(invs)

	settled := false
	for i, inv := range invs {
		others := make([]audit.Invoice, 0, len(invs)-1)
		others = append(others, invs[:i]...)
		others = append(others, invs[i+1:]...)
		v := audit.Audit(audit.Input{Shipment: *sh, Tariff: tf, Timeline: tl, Invoice: inv, Others: others, Policy: w.pol})
		if v.Decision != audit.Hold && v.Decision != audit.Reject {
			settled = true
		}
		prev, err := tx.LatestVerdict(ctx, inv.ID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		if prev != nil && prev.EvidenceHash == v.EvidenceHash {
			continue
		}
		v.Version = 1
		if prev != nil {
			v.Version = prev.Version + 1
		}
		if err := tx.SaveVerdict(ctx, v); err != nil {
			return err
		}
		w.res.Verdicts = append(w.res.Verdicts, v)
		if err := w.emit(events.TopicVerdicts, shipmentID, fmt.Sprintf("verdict:%s:%s:v%d", w.tenant, v.InvoiceID, v.Version),
			events.VerdictIssued, events.VerdictPayload{Verdict: v}); err != nil {
			return err
		}
		if err := w.rebook(shipmentID, ledger.InvoiceKey(inv.ID), ledger.VerdictLines(v)); err != nil {
			return err
		}
	}

	// Accrue what a delivered, unsettled shipment should cost; release it once an
	// invoice is booked.
	var accrual []ledger.Line
	if tl.Complete() && !settled {
		if amt, ok := audit.ExpectedCost(*sh, tf, tl, w.pol); ok {
			accrual = ledger.AccrualLines(shipmentID, amt)
		}
	}
	return w.rebook(shipmentID, ledger.AccrualKey(shipmentID), accrual)
}

func (w *work) rebook(shipmentID, key string, next []ledger.Line) error {
	ver, cur, err := w.tx.Journal(w.ctx, key)
	if err != nil {
		return err
	}
	ps := ledger.Rebook(key, ver, cur, next)
	if ps == nil {
		return nil
	}
	if err := w.tx.Post(w.ctx, key, ver+1, next, ps); err != nil {
		return err
	}
	w.res.Postings = append(w.res.Postings, ps...)
	return w.emit(events.TopicJournal, shipmentID, fmt.Sprintf("journal:%s:%s:v%d", w.tenant, key, ver+1),
		events.JournalPosted, events.JournalPayload{Postings: ps})
}

func (w *work) emit(topic, shipmentID, id string, t events.Type, payload any) error {
	env, err := events.New(id, w.tenant, t, "tollgate", shipmentID, w.at, payload)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	m := OutboxMessage{Topic: topic, Key: env.Key(), EventID: id, Payload: raw}
	w.res.Outbox = append(w.res.Outbox, m)
	return w.tx.Enqueue(w.ctx, m)
}
