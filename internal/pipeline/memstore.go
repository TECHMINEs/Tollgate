package pipeline

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"sync"

	"github.com/TECHMINEs/Tollgate/internal/audit"
	"github.com/TECHMINEs/Tollgate/internal/ledger"
	"github.com/TECHMINEs/Tollgate/internal/tariff"
	"github.com/TECHMINEs/Tollgate/internal/timeline"
)

// Memory is an in-process Store with the same semantics as the Postgres store:
// tenant-scoped state and all-or-nothing transactions. It backs the unit tests and the
// browser demo (compiled to WebAssembly).
type Memory struct {
	mu      sync.Mutex
	tenants map[string]*memTenant
}

type journalHead struct {
	version int
	lines   []ledger.Line
}

type memTenant struct {
	name      string
	policy    audit.Policy
	inbox     map[string]bool
	tariffs   map[string]tariff.Tariff
	shipments map[string]audit.Shipment
	stops     map[string][]timeline.Event
	invoices  map[string]map[string]audit.Invoice // shipment -> "id@rev" -> invoice
	verdicts  map[string][]audit.Verdict          // invoice -> every version
	journals  map[string]journalHead
	postings  []ledger.Posting
	outbox    []OutboxMessage
}

// NewMemory returns an empty store.
func NewMemory() *Memory { return &Memory{tenants: map[string]*memTenant{}} }

// AddTenant provisions a tenant.
func (m *Memory) AddTenant(id, name string, pol audit.Policy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tenants[id] = &memTenant{
		name: name, policy: pol,
		inbox: map[string]bool{}, tariffs: map[string]tariff.Tariff{}, shipments: map[string]audit.Shipment{},
		stops: map[string][]timeline.Event{}, invoices: map[string]map[string]audit.Invoice{},
		verdicts: map[string][]audit.Verdict{}, journals: map[string]journalHead{},
	}
}

// InTenant runs fn against one tenant's state. If fn fails, every write it made is undone.
func (m *Memory) InTenant(_ context.Context, tenantID string, fn func(Tx) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[tenantID]
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownTenant, tenantID)
	}
	tx := &memTx{t: t}
	if err := fn(tx); err != nil {
		for i := len(tx.undo) - 1; i >= 0; i-- {
			tx.undo[i]()
		}
		return err
	}
	return nil
}

type memTx struct {
	t    *memTenant
	undo []func()
}

func setUndo[K comparable, V any](tx *memTx, m map[K]V, k K, v V) {
	old, had := m[k]
	m[k] = v
	tx.undo = append(tx.undo, func() {
		if had {
			m[k] = old
		} else {
			delete(m, k)
		}
	})
}

func tkey(id string, v int) string { return fmt.Sprintf("%s@%d", id, v) }

func (tx *memTx) Policy(context.Context) (audit.Policy, error) { return tx.t.policy, nil }

func (tx *memTx) ClaimEvent(_ context.Context, source, id string) (bool, error) {
	k := source + "/" + id
	if tx.t.inbox[k] {
		return false, nil
	}
	setUndo(tx, tx.t.inbox, k, true)
	return true, nil
}

func (tx *memTx) LockShipment(context.Context, string) error { return nil } // the store mutex serialises everything

func (tx *memTx) PutTariff(_ context.Context, t tariff.Tariff) (*tariff.Tariff, error) {
	k := tkey(t.ID, t.Version)
	if cur, ok := tx.t.tariffs[k]; ok {
		return &cur, nil
	}
	setUndo(tx, tx.t.tariffs, k, t)
	return nil, nil
}

func (tx *memTx) Tariff(_ context.Context, id string, v int) (*tariff.Tariff, error) {
	t, ok := tx.t.tariffs[tkey(id, v)]
	if !ok {
		return nil, ErrNotFound
	}
	return &t, nil
}

func (tx *memTx) ShipmentsOnTariff(_ context.Context, id string, v int) ([]string, error) {
	var out []string
	for _, s := range tx.t.shipments {
		if s.TariffID == id && s.TariffVersion == v {
			out = append(out, s.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (tx *memTx) PutShipment(_ context.Context, s audit.Shipment) (*audit.Shipment, error) {
	if cur, ok := tx.t.shipments[s.ID]; ok {
		return &cur, nil
	}
	setUndo(tx, tx.t.shipments, s.ID, s)
	return nil, nil
}

func (tx *memTx) Shipment(_ context.Context, id string) (*audit.Shipment, error) {
	s, ok := tx.t.shipments[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &s, nil
}

func (tx *memTx) AddStopEvent(_ context.Context, shipmentID string, e timeline.Event) error {
	setUndo(tx, tx.t.stops, shipmentID, append(slices.Clone(tx.t.stops[shipmentID]), e))
	return nil
}

func (tx *memTx) StopEvents(_ context.Context, shipmentID string) ([]timeline.Event, error) {
	return slices.Clone(tx.t.stops[shipmentID]), nil
}

func (tx *memTx) PutInvoice(_ context.Context, inv audit.Invoice) (*audit.Invoice, error) {
	byID := tx.t.invoices[inv.ShipmentID]
	if cur, ok := byID[tkey(inv.ID, inv.Revision)]; ok {
		return &cur, nil
	}
	next := map[string]audit.Invoice{}
	for k, v := range byID {
		next[k] = v
	}
	next[tkey(inv.ID, inv.Revision)] = inv
	setUndo(tx, tx.t.invoices, inv.ShipmentID, next)
	return nil, nil
}

func (tx *memTx) Invoices(_ context.Context, shipmentID string) ([]audit.Invoice, error) {
	latest := map[string]audit.Invoice{}
	for _, inv := range tx.t.invoices[shipmentID] {
		if cur, ok := latest[inv.ID]; !ok || inv.Revision > cur.Revision {
			latest[inv.ID] = inv
		}
	}
	out := make([]audit.Invoice, 0, len(latest))
	for _, inv := range latest {
		out = append(out, inv)
	}
	audit.SortInvoices(out)
	return out, nil
}

func (tx *memTx) LatestVerdict(_ context.Context, invoiceID string) (*audit.Verdict, error) {
	vs := tx.t.verdicts[invoiceID]
	if len(vs) == 0 {
		return nil, ErrNotFound
	}
	v := vs[len(vs)-1]
	return &v, nil
}

func (tx *memTx) SaveVerdict(_ context.Context, v audit.Verdict) error {
	vs := tx.t.verdicts[v.InvoiceID]
	if n := len(vs); (n == 0 && v.Version != 1) || (n > 0 && v.Version != vs[n-1].Version+1) {
		return fmt.Errorf("verdict %s v%d is out of sequence", v.InvoiceID, v.Version)
	}
	setUndo(tx, tx.t.verdicts, v.InvoiceID, append(slices.Clone(vs), v))
	return nil
}

func (tx *memTx) Journal(_ context.Context, key string) (int, []ledger.Line, error) {
	h := tx.t.journals[key]
	return h.version, slices.Clone(h.lines), nil
}

func (tx *memTx) Post(_ context.Context, key string, version int, lines []ledger.Line, ps []ledger.Posting) error {
	if h := tx.t.journals[key]; h.version+1 != version {
		return fmt.Errorf("journal %s: version %d does not follow %d", key, version, h.version)
	}
	var all []ledger.Line
	for _, p := range ps {
		all = append(all, p.Line)
	}
	if !ledger.Balanced(all) {
		return fmt.Errorf("journal %s v%d does not balance", key, version)
	}
	setUndo(tx, tx.t.journals, key, journalHead{version: version, lines: slices.Clone(lines)})
	n := len(tx.t.postings)
	tx.t.postings = append(tx.t.postings, ps...)
	tx.undo = append(tx.undo, func() { tx.t.postings = tx.t.postings[:n] })
	return nil
}

func (tx *memTx) Enqueue(_ context.Context, msg OutboxMessage) error {
	n := len(tx.t.outbox)
	tx.t.outbox = append(tx.t.outbox, msg)
	tx.undo = append(tx.undo, func() { tx.t.outbox = tx.t.outbox[:n] })
	return nil
}

// Snapshot is a read-only copy of one tenant's state.
type Snapshot struct {
	TenantID  string                      `json:"tenant_id"`
	Name      string                      `json:"name"`
	Shipments []audit.Shipment            `json:"shipments"`
	Stops     map[string][]timeline.Event `json:"stops"`
	Verdicts  map[string][]audit.Verdict  `json:"verdicts"`
	Postings  []ledger.Posting            `json:"postings"`
	Outbox    int                         `json:"outbox"`
	Tariffs   []tariff.Tariff             `json:"tariffs"`
	Policy    audit.Policy                `json:"policy"`
}

// Snapshot copies one tenant's state.
func (m *Memory) Snapshot(tenantID string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tenants[tenantID]
	if !ok {
		return Snapshot{}, ErrUnknownTenant
	}
	s := Snapshot{TenantID: tenantID, Name: t.name, Stops: map[string][]timeline.Event{}, Verdicts: map[string][]audit.Verdict{},
		Postings: slices.Clone(t.postings), Outbox: len(t.outbox), Policy: t.policy}
	for _, sh := range t.shipments {
		s.Shipments = append(s.Shipments, sh)
	}
	slices.SortFunc(s.Shipments, func(a, b audit.Shipment) int { return a.TenderedAt.Compare(b.TenderedAt) })
	for k, v := range t.stops {
		s.Stops[k] = slices.Clone(v)
	}
	for k, v := range t.verdicts {
		s.Verdicts[k] = slices.Clone(v)
	}
	for _, tf := range t.tariffs {
		s.Tariffs = append(s.Tariffs, tf)
	}
	slices.SortFunc(s.Tariffs, func(a, b tariff.Tariff) int {
		if a.ID != b.ID {
			if a.ID < b.ID {
				return -1
			}
			return 1
		}
		return a.Version - b.Version
	})
	return s, nil
}
