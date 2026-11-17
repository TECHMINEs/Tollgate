// Package events defines the messages that flow through the broker.
//
// Every inbound document for a shipment (tender, stop reports, invoices) travels on one
// topic keyed by tenant and shipment, so the broker delivers a shipment's documents to
// one consumer in offset order. Offset order is still not event-time order: telematics
// batches and EDI files arrive late, so the engine never relies on it.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

// Topics. The .v1 suffix is the schema version; a breaking change gets a new topic.
const (
	TopicFreight  = "tollgate.freight.v1"     // inbound: tariffs, tenders, stop reports, invoices
	TopicVerdicts = "tollgate.verdicts.v1"    // outbound: audit verdicts
	TopicJournal  = "tollgate.journal.v1"     // outbound: ledger postings
	TopicDLQ      = "tollgate.freight.dlq.v1" // inbound messages that can never be processed
)

// Type is the kind of document an envelope carries.
type Type string

const (
	TariffPublished  Type = "tariff.published"
	ShipmentTendered Type = "shipment.tendered"
	StopReported     Type = "stop.reported"
	InvoiceReceived  Type = "invoice.received"

	VerdictIssued Type = "verdict.issued"
	JournalPosted Type = "journal.posted"
)

// Envelope wraps every message on every topic.
type Envelope struct {
	EventID    string          `json:"event_id"` // unique per source; the idempotency key
	TenantID   string          `json:"tenant_id"`
	Type       Type            `json:"type"`
	Source     string          `json:"source"`
	ShipmentID string          `json:"shipment_id,omitempty"`
	OccurredAt time.Time       `json:"occurred_at"`
	Payload    json.RawMessage `json:"payload"`
}

// Key is the partition key: tenant and shipment (or tariff), so one aggregate's
// messages share a partition and are never processed concurrently.
func (e Envelope) Key() string {
	if e.ShipmentID != "" {
		return e.TenantID + "/" + e.ShipmentID
	}
	return e.TenantID + "/" + string(e.Type)
}

// Payloads.
type (
	TariffPayload struct {
		Tariff tariff.Tariff `json:"tariff"`
	}
	ShipmentPayload struct {
		Shipment audit.Shipment `json:"shipment"`
	}
	StopPayload struct {
		StopSeq int           `json:"stop_seq"`
		Kind    timeline.Kind `json:"kind"`
		At      time.Time     `json:"at"`
	}
	InvoicePayload struct {
		Invoice audit.Invoice `json:"invoice"`
	}
	VerdictPayload struct {
		Verdict audit.Verdict `json:"verdict"`
	}
	JournalPayload struct {
		Postings []ledger.Posting `json:"postings"`
	}
)

// New builds an envelope around a payload.
func New(id, tenant string, t Type, source, shipment string, at time.Time, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{EventID: id, TenantID: tenant, Type: t, Source: source, ShipmentID: shipment, OccurredAt: at.UTC(), Payload: raw}, nil
}

// Must is New for fixtures and simulators.
func Must(id, tenant string, t Type, source, shipment string, at time.Time, payload any) Envelope {
	e, err := New(id, tenant, t, source, shipment, at, payload)
	if err != nil {
		panic(err)
	}
	return e
}

// Decode parses and validates a broker message.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("envelope is not valid JSON: %w", err)
	}
	return e, e.Validate()
}

var validTenant = func(s string) bool {
	if len(s) < 2 || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// Validate checks the envelope fields every handler relies on.
func (e Envelope) Validate() error {
	var errs []error
	if strings.TrimSpace(e.EventID) == "" {
		errs = append(errs, errors.New("event_id is required"))
	}
	if !validTenant(e.TenantID) {
		errs = append(errs, fmt.Errorf("tenant_id %q must be 2-63 chars of a-z, 0-9 and -", e.TenantID))
	}
	if e.Source == "" {
		errs = append(errs, errors.New("source is required"))
	}
	switch e.Type {
	case TariffPublished:
	case ShipmentTendered, StopReported, InvoiceReceived:
		if e.ShipmentID == "" {
			errs = append(errs, fmt.Errorf("%s needs shipment_id", e.Type))
		}
	default:
		errs = append(errs, fmt.Errorf("unknown type %q", e.Type))
	}
	if len(e.Payload) == 0 {
		errs = append(errs, errors.New("payload is required"))
	}
	return errors.Join(errs...)
}
