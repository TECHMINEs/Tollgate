package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TECHMINEs/Tollgate/internal/audit"
)

// UpsertTenant provisions a tenant. It runs on the owner connection: tenant
// provisioning is an administrative act, never something an inbound event can do.
func UpsertTenant(ctx context.Context, owner *pgxpool.Pool, id, name string, pol audit.Policy) error {
	raw, err := json.Marshal(pol)
	if err != nil {
		return err
	}
	_, err = owner.Exec(ctx, `INSERT INTO tenants (id, name, policy) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, policy = EXCLUDED.policy`, id, name, raw)
	return err
}

// OutboxRow is an unpublished downstream message.
type OutboxRow struct {
	ID      int64
	Topic   string
	Key     string
	EventID string
	Payload []byte
}

// RelayOutbox claims up to limit unpublished rows, hands them to publish, and marks them
// published only if publish succeeds. Run it as tollgate_publisher. Rows are claimed
// with SKIP LOCKED, so a second relay instance never blocks; a crash between publish
// and commit re-sends the batch, which consumers drop by event ID.
func RelayOutbox(ctx context.Context, pool *pgxpool.Pool, limit int, publish func(context.Context, []OutboxRow) error) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, topic, key, event_id, payload FROM outbox
			WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (OutboxRow, error) {
			var o OutboxRow
			return o, r.Scan(&o.ID, &o.Topic, &o.Key, &o.EventID, &o.Payload)
		})
		if err != nil || len(batch) == 0 {
			return err
		}
		if err := publish(ctx, batch); err != nil {
			return err
		}
		ids := make([]int64, len(batch))
		for i, o := range batch {
			ids[i] = o.ID
		}
		n = len(batch)
		_, err = tx.Exec(ctx, `UPDATE outbox SET published_at = now() WHERE id = ANY($1)`, ids)
		return err
	})
	return n, err
}
