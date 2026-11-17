package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/pipeline"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

// Postgres implements pipeline.Store. Connect it as tollgate_app so row-level security
// applies to every query.
type Postgres struct {
	Pool *pgxpool.Pool
}

var _ pipeline.Store = (*Postgres)(nil)

// Connect opens a pool and checks it.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// InTenant runs fn in a transaction scoped to one tenant.
func (p *Postgres) InTenant(ctx context.Context, tenantID string, fn func(pipeline.Tx) error) error {
	return pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		// is_local = true: the setting ends with this transaction, so it cannot leak
		// to the next user of the pooled connection.
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			return err
		}
		return fn(&pgTx{tx: tx, tenant: tenantID})
	})
}

type pgTx struct {
	tx     pgx.Tx
	tenant string
}

func scanJSON[T any](row pgx.Row) (*T, error) {
	var raw []byte
	if err := row.Scan(&raw); errors.Is(err, pgx.ErrNoRows) {
		return nil, pipeline.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (t *pgTx) Policy(ctx context.Context) (audit.Policy, error) {
	pol, err := scanJSON[audit.Policy](t.tx.QueryRow(ctx, `SELECT policy FROM tenants WHERE id = app_tenant()`))
	if errors.Is(err, pipeline.ErrNotFound) {
		return audit.Policy{}, fmt.Errorf("%w %q", pipeline.ErrUnknownTenant, t.tenant)
	} else if err != nil {
		return audit.Policy{}, err
	}
	return *pol, nil
}

func (t *pgTx) ClaimEvent(ctx context.Context, source, eventID string) (bool, error) {
	tag, err := t.tx.Exec(ctx, `INSERT INTO inbox (tenant_id, source, event_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		t.tenant, source, eventID)
	return tag.RowsAffected() == 1, err
}

func (t *pgTx) LockShipment(ctx context.Context, shipmentID string) error {
	// An advisory lock works before the shipment row exists (stop events can come first).
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, t.tenant+"/"+shipmentID)
	return err
}

func (t *pgTx) PutTariff(ctx context.Context, tf tariff.Tariff) (*tariff.Tariff, error) {
	body, err := json.Marshal(tf)
	if err != nil {
		return nil, err
	}
	tag, err := t.tx.Exec(ctx, `INSERT INTO tariffs (tenant_id, id, version, carrier_id, body) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT DO NOTHING`, t.tenant, tf.ID, tf.Version, tf.CarrierID, body)
	if err != nil || tag.RowsAffected() == 1 {
		return nil, err
	}
	return t.Tariff(ctx, tf.ID, tf.Version)
}

func (t *pgTx) Tariff(ctx context.Context, id string, version int) (*tariff.Tariff, error) {
	return scanJSON[tariff.Tariff](t.tx.QueryRow(ctx, `SELECT body FROM tariffs WHERE id = $1 AND version = $2`, id, version))
}

func (t *pgTx) ShipmentsOnTariff(ctx context.Context, id string, version int) ([]string, error) {
	rows, err := t.tx.Query(ctx, `SELECT id FROM shipments WHERE tariff_id = $1 AND tariff_version = $2 ORDER BY id`, id, version)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (t *pgTx) PutShipment(ctx context.Context, s audit.Shipment) (*audit.Shipment, error) {
	body, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	tag, err := t.tx.Exec(ctx, `INSERT INTO shipments (tenant_id, id, carrier_id, tariff_id, tariff_version, lane_id, tendered_at, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT DO NOTHING`,
		t.tenant, s.ID, s.CarrierID, s.TariffID, s.TariffVersion, s.LaneID, s.TenderedAt, body)
	if err != nil || tag.RowsAffected() == 1 {
		return nil, err
	}
	return t.Shipment(ctx, s.ID)
}

func (t *pgTx) Shipment(ctx context.Context, id string) (*audit.Shipment, error) {
	return scanJSON[audit.Shipment](t.tx.QueryRow(ctx, `SELECT body FROM shipments WHERE id = $1`, id))
}

func (t *pgTx) AddStopEvent(ctx context.Context, shipmentID string, e timeline.Event) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO stop_events (tenant_id, source, event_id, shipment_id, stop_seq, kind, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`,
		t.tenant, string(e.Source), e.EventID, shipmentID, e.StopSeq, string(e.Kind), e.At)
	return err
}

func (t *pgTx) StopEvents(ctx context.Context, shipmentID string) ([]timeline.Event, error) {
	rows, err := t.tx.Query(ctx, `SELECT event_id, source, stop_seq, kind, occurred_at FROM stop_events WHERE shipment_id = $1`, shipmentID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (timeline.Event, error) {
		var e timeline.Event
		var src, kind string
		err := r.Scan(&e.EventID, &src, &e.StopSeq, &kind, &e.At)
		e.Source, e.Kind, e.At = timeline.Source(src), timeline.Kind(kind), e.At.UTC()
		return e, err
	})
}

func (t *pgTx) PutInvoice(ctx context.Context, inv audit.Invoice) (*audit.Invoice, error) {
	body, err := json.Marshal(inv)
	if err != nil {
		return nil, err
	}
	tag, err := t.tx.Exec(ctx, `INSERT INTO invoices (tenant_id, id, revision, shipment_id, carrier_id, number, received_at, billed_cents, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING`,
		t.tenant, inv.ID, inv.Revision, inv.ShipmentID, inv.CarrierID, inv.Number, inv.ReceivedAt, int64(inv.BilledCents()), body)
	if err != nil || tag.RowsAffected() == 1 {
		return nil, err
	}
	return scanJSON[audit.Invoice](t.tx.QueryRow(ctx, `SELECT body FROM invoices WHERE id = $1 AND revision = $2`, inv.ID, inv.Revision))
}

func (t *pgTx) Invoices(ctx context.Context, shipmentID string) ([]audit.Invoice, error) {
	rows, err := t.tx.Query(ctx, `SELECT DISTINCT ON (id) body FROM invoices WHERE shipment_id = $1 ORDER BY id, revision DESC`, shipmentID)
	if err != nil {
		return nil, err
	}
	invs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (audit.Invoice, error) {
		v, err := scanJSON[audit.Invoice](r)
		if err != nil {
			return audit.Invoice{}, err
		}
		return *v, nil
	})
	audit.SortInvoices(invs)
	return invs, err
}

func (t *pgTx) LatestVerdict(ctx context.Context, invoiceID string) (*audit.Verdict, error) {
	return scanJSON[audit.Verdict](t.tx.QueryRow(ctx, `SELECT body FROM verdicts WHERE invoice_id = $1 ORDER BY version DESC LIMIT 1`, invoiceID))
}

func (t *pgTx) SaveVerdict(ctx context.Context, v audit.Verdict) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `INSERT INTO verdicts (tenant_id, invoice_id, version, shipment_id, invoice_revision, carrier_id, decision,
		evidence_hash, billed_cents, approved_cents, disputed_cents, leakage_cents, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		t.tenant, v.InvoiceID, v.Version, v.ShipmentID, v.InvoiceRevision, v.CarrierID, string(v.Decision), v.EvidenceHash,
		int64(v.BilledCents), int64(v.ApprovedCents), int64(v.DisputedCents), int64(v.LeakageCents), body)
	return err
}

func (t *pgTx) Journal(ctx context.Context, key string) (int, []ledger.Line, error) {
	var version int
	var raw []byte
	err := t.tx.QueryRow(ctx, `SELECT version, lines FROM journals WHERE journal_key = $1`, key).Scan(&version, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, nil
	} else if err != nil {
		return 0, nil, err
	}
	var lines []ledger.Line
	return version, lines, json.Unmarshal(raw, &lines)
}

func (t *pgTx) Post(ctx context.Context, key string, version int, lines []ledger.Line, ps []ledger.Posting) error {
	if lines == nil {
		lines = []ledger.Line{}
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	// Optimistic check: only advance the head from exactly the version we read.
	tag, err := t.tx.Exec(ctx, `INSERT INTO journals (tenant_id, journal_key, version, lines) VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, journal_key) DO UPDATE SET version = EXCLUDED.version, lines = EXCLUDED.lines, updated_at = now()
		WHERE journals.version = EXCLUDED.version - 1`, t.tenant, key, version, raw)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("journal %s moved past v%d concurrently", key, version-1)
	}
	b := &pgx.Batch{}
	for _, p := range ps {
		b.Queue(`INSERT INTO postings (tenant_id, journal_key, version, line_no, account, debit_cents, credit_cents, memo)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			t.tenant, p.JournalKey, p.Version, p.LineNo, string(p.Account), int64(p.Debit), int64(p.Credit), p.Memo)
	}
	return t.tx.SendBatch(ctx, b).Close()
}

func (t *pgTx) Enqueue(ctx context.Context, m pipeline.OutboxMessage) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO outbox (tenant_id, topic, key, event_id, payload) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (event_id) DO NOTHING`, t.tenant, m.Topic, m.Key, m.EventID, []byte(m.Payload))
	return err
}
