package store_test

// Integration tests against a real Postgres. They run when TOLLGATE_TEST_ADMIN_URL is
// set (make test-integration does this against docker-compose) and skip otherwise.
// Each run creates and drops its own database.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/pipeline"
	"github.com/TECHMINEs/Tollgate/internal/sim"
	"github.com/TECHMINEs/Tollgate/internal/store"
)

type env struct {
	owner, app, publisher *pgxpool.Pool
}

func withDB(t *testing.T, u string, db string) string {
	p, err := url.Parse(u)
	if err != nil {
		t.Fatal(err)
	}
	p.Path = "/" + db
	return p.String()
}

func withUser(t *testing.T, u, user, pass string) string {
	p, _ := url.Parse(u)
	p.User = url.UserPassword(user, pass)
	return p.String()
}

func setup(t *testing.T) env {
	t.Helper()
	admin := os.Getenv("TOLLGATE_TEST_ADMIN_URL")
	if admin == "" {
		t.Skip("TOLLGATE_TEST_ADMIN_URL not set; run `make test-integration`")
	}
	ctx := context.Background()
	root, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	db := fmt.Sprintf("tollgate_test_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, "CREATE DATABASE "+db); err != nil {
		t.Fatal(err)
	}
	connect := func(u string) *pgxpool.Pool {
		pool, err := store.Connect(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return pool
	}
	e := env{owner: connect(withDB(t, admin, db))}
	if _, err := store.Migrate(ctx, e.owner); err != nil {
		t.Fatal(err)
	}
	e.app = connect(withUser(t, withDB(t, admin, db), "tollgate_app", envOr("TOLLGATE_TEST_APP_PASSWORD", "tollgate_app_dev")))
	e.publisher = connect(withUser(t, withDB(t, admin, db), "tollgate_publisher", envOr("TOLLGATE_TEST_PUBLISHER_PASSWORD", "tollgate_publisher_dev")))
	t.Cleanup(func() {
		e.app.Close()
		e.publisher.Close()
		e.owner.Close()
		_, _ = root.Exec(context.Background(), "DROP DATABASE "+db+" WITH (FORCE)")
		_ = root.Close(context.Background())
	})
	return e
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func scenario(t *testing.T, e env) sim.Scenario {
	t.Helper()
	cfg := sim.DefaultConfig()
	cfg.ShipmentsPerTenant = 12
	sc := sim.Generate(cfg)
	for _, tn := range sc.Tenants {
		if err := store.UpsertTenant(context.Background(), e.owner, tn.ID, tn.Name, tn.Policy); err != nil {
			t.Fatal(err)
		}
	}
	return sc
}

func process(t *testing.T, s pipeline.Store, sc sim.Scenario) {
	t.Helper()
	p := &pipeline.Processor{Store: s}
	for _, m := range sc.Messages {
		if _, err := p.Handle(context.Background(), m.Envelope); err != nil {
			t.Fatalf("%s: %v", m.Envelope.EventID, err)
		}
	}
}

func TestPostgresMatchesMemory(t *testing.T) {
	e := setup(t)
	sc := scenario(t, e)
	process(t, &store.Postgres{Pool: e.app}, sc)

	mem := pipeline.NewMemory()
	for _, tn := range sc.Tenants {
		mem.AddTenant(tn.ID, tn.Name, tn.Policy)
	}
	process(t, mem, sc)

	ctx := context.Background()
	pg := &store.Postgres{Pool: e.app}
	for id, truth := range sc.Truth {
		snap, _ := mem.Snapshot(truth.TenantID)
		vs := snap.Verdicts[id]
		want := vs[len(vs)-1]
		var got *audit.Verdict
		err := pg.InTenant(ctx, truth.TenantID, func(tx pipeline.Tx) error {
			var err error
			got, err = tx.LatestVerdict(ctx, id)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("%s: postgres %s v%d, memory %s v%d", id, got.Decision, got.Version, want.Decision, want.Version)
		}
	}
}

func TestRowLevelSecurity(t *testing.T) {
	e := setup(t)
	sc := scenario(t, e)
	process(t, &store.Postgres{Pool: e.app}, sc)
	ctx := context.Background()
	a, b := sc.Tenants[0].ID, sc.Tenants[1].ID

	var super, bypass bool
	if err := e.app.QueryRow(ctx, `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatal("tollgate_app must not be able to bypass row-level security")
	}

	count := func(tenant, table string) (n int) {
		err := pgx.BeginFunc(ctx, e.app, func(tx pgx.Tx) error {
			if tenant != "" {
				if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
					return err
				}
			}
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, table := range []string{"shipments", "invoices", "verdicts", "postings", "stop_events", "outbox"} {
		if n := count("", table); n != 0 {
			t.Errorf("%s: %d rows visible with no tenant set", table, n)
		}
		na, nb := count(a, table), count(b, table)
		if na == 0 || nb == 0 {
			t.Errorf("%s: expected rows for both tenants, got %d and %d", table, na, nb)
		}
		var foreign int
		_ = pgx.BeginFunc(ctx, e.app, func(tx pgx.Tx) error {
			_, _ = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, a)
			return tx.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE tenant_id = $1`, b).Scan(&foreign)
		})
		if foreign != 0 {
			t.Errorf("%s: tenant %s can see %d rows of %s", table, a, foreign, b)
		}
	}

	// Writing a row for another tenant fails the policy's WITH CHECK.
	err := pgx.BeginFunc(ctx, e.app, func(tx pgx.Tx) error {
		_, _ = tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, a)
		_, err := tx.Exec(ctx, `INSERT INTO inbox (tenant_id, source, event_id) VALUES ($1, 'x', 'forged')`, b)
		return err
	})
	if !isCode(err, "42501") {
		t.Fatalf("cross-tenant insert should violate row-level security, got %v", err)
	}

	// The scope ends with the transaction: the next transaction on a pooled connection sees nothing.
	if n := count("", "verdicts"); n != 0 {
		t.Fatalf("tenant scope leaked across transactions: %d rows", n)
	}

	// The publisher relays every tenant's outbox but can read nothing else.
	var outboxRows int
	if err := e.publisher.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&outboxRows); err != nil || outboxRows == 0 {
		t.Fatalf("publisher outbox read: %d rows, %v", outboxRows, err)
	}
	if _, err := e.publisher.Exec(ctx, `SELECT count(*) FROM verdicts`); !isCode(err, "42501") {
		t.Fatalf("publisher must not read verdicts, got %v", err)
	}
}

func TestLedgerIsAppendOnlyAndBalanced(t *testing.T) {
	e := setup(t)
	sc := scenario(t, e)
	process(t, &store.Postgres{Pool: e.app}, sc)
	ctx := context.Background()
	tenant := sc.Tenants[0].ID
	inTenant := func(fn func(pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, e.app, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
				return err
			}
			return fn(tx)
		})
	}

	err := inTenant(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE postings SET debit_cents = debit_cents + 1 WHERE debit_cents > 0`)
		return err
	})
	if err == nil {
		t.Fatal("updating a posting must fail")
	}

	err = inTenant(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO postings (tenant_id, journal_key, version, line_no, account, debit_cents, memo)
			VALUES ($1, 'manual:1', 1, 1, $2, 500, 'one-sided')`, tenant, string(ledger.FreightExpense))
		return err
	})
	if !isCode(err, "23514") {
		t.Fatalf("an unbalanced journal must fail at commit, got %v", err)
	}

	var net int64
	if err := inTenant(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sum(debit_cents - credit_cents), 0) FROM postings`).Scan(&net)
	}); err != nil || net != 0 {
		t.Fatalf("trial balance off by %d (%v)", net, err)
	}
}

func TestReplayAndOutboxRelay(t *testing.T) {
	e := setup(t)
	sc := scenario(t, e)
	pg := &store.Postgres{Pool: e.app}
	process(t, pg, sc)
	ctx := context.Background()

	var before int
	_ = e.publisher.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&before)
	process(t, pg, sc) // full redelivery
	var after int
	_ = e.publisher.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&after)
	if before != after {
		t.Fatalf("replay added %d outbox rows", after-before)
	}

	sent := map[string]bool{}
	for {
		n, err := store.RelayOutbox(ctx, e.publisher, 50, func(_ context.Context, rows []store.OutboxRow) error {
			for _, r := range rows {
				sent[r.EventID] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if len(sent) != before {
		t.Fatalf("relayed %d of %d outbox rows", len(sent), before)
	}

	// A failed publish leaves rows unpublished for the next attempt.
	process(t, pg, sim.Generate(sim.Config{Seed: 2, ShipmentsPerTenant: 2, Start: sc.Config.Start}))
	_, err := store.RelayOutbox(ctx, e.publisher, 50, func(context.Context, []store.OutboxRow) error { return errors.New("broker down") })
	if err == nil {
		t.Fatal("publish error should surface")
	}
	var pending int
	_ = e.publisher.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending)
	if pending == 0 {
		t.Fatal("rows must stay pending after a failed publish")
	}
}

func isCode(err error, code string) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == code
}
