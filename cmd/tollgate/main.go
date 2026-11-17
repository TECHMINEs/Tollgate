// Command tollgate runs the freight audit pipeline.
//
//	tollgate setup                   migrate + seed + topics (what docker-compose runs once)
//	tollgate migrate                 apply SQL migrations (schema owner)
//	tollgate seed                    provision the demo tenants (schema owner)
//	tollgate topics                  create the broker topics
//	tollgate auditor                 consume freight documents and audit them
//	tollgate outbox                  relay verdicts and journal postings to the broker
//	tollgate simulate [flags]        produce a synthetic freight scenario to the broker
//	tollgate report                  per-tenant totals, read through row-level security
//	tollgate demo [flags]            run a scenario in memory, no infrastructure needed
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/broker"
	"github.com/TECHMINEs/Tollgate/internal/events"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/pipeline"
	"github.com/TECHMINEs/Tollgate/internal/sim"
	"github.com/TECHMINEs/Tollgate/internal/store"
)

// Local development defaults; they match docker-compose.yml. Override with env vars.
var (
	adminURL     = env("TOLLGATE_ADMIN_URL", "postgres://tollgate:tollgate_dev@localhost:5432/tollgate")
	appURL       = env("TOLLGATE_DATABASE_URL", "postgres://tollgate_app:tollgate_app_dev@localhost:5432/tollgate")
	publisherURL = env("TOLLGATE_PUBLISHER_URL", "postgres://tollgate_publisher:tollgate_publisher_dev@localhost:5432/tollgate")
	brokers      = broker.Brokers(env("TOLLGATE_BROKERS", "localhost:19092"))
	healthAddr   = env("TOLLGATE_HEALTH_ADDR", "")
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tollgate setup|migrate|seed|topics|auditor|outbox|simulate|report|demo")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmds := map[string]func(context.Context, *slog.Logger, []string) error{
		"setup": setup, "migrate": migrate, "seed": seed, "topics": topics, "auditor": auditor,
		"outbox": outbox, "simulate": simulate, "report": report, "demo": demo,
	}
	run, ok := cmds[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
	if err := run(ctx, log, os.Args[2:]); err != nil && !errors.Is(err, context.Canceled) {
		log.Error(os.Args[1]+" failed", "err", err)
		os.Exit(1)
	}
}

func setup(ctx context.Context, log *slog.Logger, args []string) error {
	for _, step := range []func(context.Context, *slog.Logger, []string) error{migrate, seed, topics} {
		if err := step(ctx, log, args); err != nil {
			return err
		}
	}
	return nil
}

func migrate(ctx context.Context, log *slog.Logger, _ []string) error {
	pool, err := store.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	applied, err := store.Migrate(ctx, pool)
	log.Info("migrations", "applied", applied)
	return err
}

func seed(ctx context.Context, log *slog.Logger, _ []string) error {
	pool, err := store.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, t := range sim.Generate(sim.Config{ShipmentsPerTenant: 0}).Tenants {
		if err := store.UpsertTenant(ctx, pool, t.ID, t.Name, t.Policy); err != nil {
			return err
		}
		log.Info("tenant ready", "id", t.ID, "name", t.Name)
	}
	return nil
}

func topics(ctx context.Context, log *slog.Logger, _ []string) error {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return err
	}
	defer cl.Close()
	if err := broker.EnsureTopics(ctx, cl, 1); err != nil {
		return err
	}
	for _, t := range broker.Topics {
		log.Info("topic ready", "name", t.Name, "partitions", t.Partitions)
	}
	return nil
}

func serveHealth(log *slog.Logger, check func(context.Context) error) {
	if healthAddr == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := check(ctx); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	go func() {
		srv := &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			log.Error("health server", "err", err)
		}
	}()
}

func auditor(ctx context.Context, log *slog.Logger, _ []string) error {
	pool, err := store.Connect(ctx, appURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	cl, err := broker.NewConsumerClient(brokers, env("TOLLGATE_GROUP", "tollgate-auditor"))
	if err != nil {
		return err
	}
	defer cl.Close()
	serveHealth(log, func(ctx context.Context) error { return pool.Ping(ctx) })

	proc := &pipeline.Processor{Store: &store.Postgres{Pool: pool}}
	c := &broker.Consumer{
		Client: cl, Log: log, IsPermanent: pipeline.IsPermanent,
		Handle: func(ctx context.Context, r *kgo.Record) error {
			env, err := events.Decode(r.Value)
			if err != nil {
				return pipeline.PermanentError{Err: err}
			}
			res, err := proc.Handle(ctx, env)
			if err != nil {
				return err
			}
			for _, v := range res.Verdicts {
				log.Info("verdict", "tenant", v.TenantID, "invoice", v.InvoiceID, "v", v.Version, "decision", v.Decision,
					"approved", v.ApprovedCents.String(), "disputed", v.DisputedCents.String())
			}
			if res.Duplicate {
				log.Debug("duplicate dropped", "event", env.EventID)
			}
			return nil
		},
	}
	log.Info("auditor consuming", "topic", events.TopicFreight, "brokers", brokers)
	return c.Run(ctx)
}

func outbox(ctx context.Context, log *slog.Logger, _ []string) error {
	pool, err := store.Connect(ctx, publisherURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return err
	}
	defer cl.Close()
	serveHealth(log, func(ctx context.Context) error { return pool.Ping(ctx) })
	log.Info("outbox relay running")
	for {
		n, err := store.RelayOutbox(ctx, pool, 200, func(ctx context.Context, rows []store.OutboxRow) error {
			recs := make([]*kgo.Record, len(rows))
			for i, o := range rows {
				recs[i] = &kgo.Record{Topic: o.Topic, Key: []byte(o.Key), Value: o.Payload,
					Headers: []kgo.RecordHeader{{Key: "event_id", Value: []byte(o.EventID)}}}
			}
			return cl.ProduceSync(ctx, recs...).FirstErr()
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Error("relay", "err", err)
		}
		if n > 0 {
			log.Info("relayed", "messages", n)
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func scenarioFlags(name string, args []string) (sim.Config, error) {
	cfg := sim.DefaultConfig()
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "scenario seed")
	fs.IntVar(&cfg.ShipmentsPerTenant, "shipments", cfg.ShipmentsPerTenant, "shipments per tenant")
	fs.IntVar(&cfg.ErrorRatePct, "errors", cfg.ErrorRatePct, "percent of invoices with an injected billing error")
	fs.IntVar(&cfg.DuplicatePct, "dupes", cfg.DuplicatePct, "percent of messages redelivered")
	fs.IntVar(&cfg.LateTelematicsPct, "late-eld", cfg.LateTelematicsPct, "percent of ELD batches arriving after the invoice")
	fs.BoolVar(&cfg.Shuffled, "shuffled", false, "deliver in random order")
	return cfg, fs.Parse(args)
}

func simulate(ctx context.Context, log *slog.Logger, args []string) error {
	cfg, err := scenarioFlags("simulate", args)
	if err != nil {
		return err
	}
	sc := sim.Generate(cfg)
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return err
	}
	defer cl.Close()
	recs := make([]*kgo.Record, 0, len(sc.Messages))
	for _, m := range sc.Messages {
		raw, err := json.Marshal(m.Envelope)
		if err != nil {
			return err
		}
		recs = append(recs, broker.Record(events.TopicFreight, m.Envelope, raw))
	}
	if err := cl.ProduceSync(ctx, recs...).FirstErr(); err != nil {
		return err
	}
	log.Info("produced scenario", "messages", len(recs), "invoices", len(sc.Truth), "seed", cfg.Seed)
	return nil
}

type tenantTotals struct {
	decisions                           map[audit.Decision]int
	billed, approved, disputed, leakage ledger.Cents
	balances                            map[ledger.Account]ledger.Cents
}

func report(ctx context.Context, _ *slog.Logger, _ []string) error {
	pool, err := store.Connect(ctx, appURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	defer w.Flush()
	fmt.Fprintln(w, "TENANT\tINVOICES\tAPPROVE\tVARIANCE\tSHORT_PAY\tREJECT\tHOLD\tBILLED\tAPPROVED\tDISPUTED\tACCRUED")
	for _, t := range sim.Generate(sim.Config{}).Tenants {
		tt, err := tenantReport(ctx, pool, t.ID)
		if err != nil {
			return err
		}
		n := 0
		for _, c := range tt.decisions {
			n += c
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s\t%s\n", t.ID, n,
			tt.decisions[audit.Approve], tt.decisions[audit.ApproveVariance], tt.decisions[audit.ShortPay],
			tt.decisions[audit.Reject], tt.decisions[audit.Hold], tt.billed, tt.approved, tt.disputed, -tt.balances[ledger.AccruedFreight])
	}
	return nil
}

func tenantReport(ctx context.Context, pool *pgxpool.Pool, tenant string) (tenantTotals, error) {
	tt := tenantTotals{decisions: map[audit.Decision]int{}, balances: map[ledger.Account]ledger.Cents{}}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (invoice_id) decision, billed_cents, approved_cents, disputed_cents, leakage_cents
			FROM verdicts ORDER BY invoice_id, version DESC`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d string
			var b, a, di, l int64
			if err := rows.Scan(&d, &b, &a, &di, &l); err != nil {
				return err
			}
			tt.decisions[audit.Decision(d)]++
			tt.billed += ledger.Cents(b)
			tt.approved += ledger.Cents(a)
			tt.disputed += ledger.Cents(di)
			tt.leakage += ledger.Cents(l)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows, err = tx.Query(ctx, `SELECT account, sum(debit_cents - credit_cents) FROM postings GROUP BY account`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var a string
			var bal int64
			if err := rows.Scan(&a, &bal); err != nil {
				return err
			}
			tt.balances[ledger.Account(a)] = ledger.Cents(bal)
		}
		return rows.Err()
	})
	return tt, err
}

func demo(ctx context.Context, _ *slog.Logger, args []string) error {
	cfg, err := scenarioFlags("demo", args)
	if err != nil {
		return err
	}
	sc := sim.Generate(cfg)
	mem := pipeline.NewMemory()
	for _, t := range sc.Tenants {
		mem.AddTenant(t.ID, t.Name, t.Policy)
	}
	proc := &pipeline.Processor{Store: mem}
	dups, versions := 0, 0
	for _, m := range sc.Messages {
		res, err := proc.Handle(ctx, m.Envelope)
		if err != nil {
			return fmt.Errorf("%s: %w", m.Envelope.EventID, err)
		}
		if res.Duplicate {
			dups++
		}
		versions += len(res.Verdicts)
	}
	fmt.Printf("%d messages (%d redelivered and dropped), %d invoices, %d verdict versions\n\n", len(sc.Messages), dups, len(sc.Truth), versions)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "INVOICE\tDECISION\tBILLED\tAPPROVED\tDISPUTED\tINJECTED ERROR")
	ids := make([]string, 0, len(sc.Truth))
	for id := range sc.Truth {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		tr := sc.Truth[id]
		s, _ := mem.Snapshot(tr.TenantID)
		vs := s.Verdicts[id]
		v := vs[len(vs)-1]
		errs := "-"
		if len(tr.Errors) > 0 {
			errs = string(tr.Errors[0])
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", id, v.Decision, v.BilledCents, v.ApprovedCents, v.DisputedCents, errs)
	}
	return w.Flush()
}
