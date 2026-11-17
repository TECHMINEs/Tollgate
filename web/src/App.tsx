import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { FastForward, Pause, Play, RotateCcw, Settings2, SkipForward } from 'lucide-react'
import { Engine, loadEngine } from './engine'
import type { Dashboard, Meta, ShipmentDetail, SimConfig } from './types'
import { money0 } from './format'
import { Stream } from './components/Stream'
import { InvoiceQueue } from './components/InvoiceQueue'
import { ShipmentDrawer } from './components/ShipmentDrawer'
import { Carriers, Ledger, PolicyLab, ReplayProof } from './components/Insights'
import { HowItWorks } from './components/HowItWorks'
import { Kpi, Label, Slider, Tabs, Toggle } from './components/ui'

const DEFAULT_CFG: SimConfig = { seed: 7, shipments_per_tenant: 24, error_rate_pct: 30, duplicate_pct: 8, late_telematics_pct: 15, shuffled: false }
const SPEEDS = [1, 3, 8, 25]
type Tab = 'invoices' | 'carriers' | 'ledger' | 'policy' | 'replay' | 'how'

export default function App() {
  const [engine, setEngine] = useState<Engine | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [cfg, setCfg] = useState<SimConfig>(DEFAULT_CFG)
  const [draft, setDraft] = useState<SimConfig>(DEFAULT_CFG)
  const [meta, setMeta] = useState<Meta | null>(null)
  const [tenant, setTenant] = useState<string>('')
  const [dash, setDash] = useState<Dashboard | null>(null)
  const [playing, setPlaying] = useState(false)
  const [speed, setSpeed] = useState(3)
  const [tab, setTab] = useState<Tab>('invoices')
  const [settings, setSettings] = useState(false)
  const [showKey, setShowKey] = useState(false)
  const [open, setOpen] = useState<string | null>(null)
  const [detail, setDetail] = useState<ShipmentDetail | null>(null)
  const tenantRef = useRef(tenant)
  tenantRef.current = tenant

  const refresh = useCallback((e: Engine, t: string) => {
    setDash(e.dashboard(t, 120))
  }, [])

  const start = useCallback((e: Engine, c: SimConfig) => {
    const m = e.start(c)
    setMeta(m)
    const t = tenantRef.current && m.tenants.some((x) => x.id === tenantRef.current) ? tenantRef.current : m.tenants[0].id
    setTenant(t)
    refresh(e, t)
    setOpen(null)
  }, [refresh])

  useEffect(() => {
    loadEngine()
      .then((raw) => {
        const e = new Engine(raw)
        setEngine(e)
        start(e, DEFAULT_CFG)
        setPlaying(!window.matchMedia('(prefers-reduced-motion: reduce)').matches)
      })
      .catch((err: Error) => setError(err.message))
  }, [start])

  const done = !!dash && dash.progress[0] >= dash.progress[1]

  const step = useCallback((n: number) => {
    if (!engine) return
    engine.step(n)
    refresh(engine, tenantRef.current)
  }, [engine, refresh])

  useEffect(() => {
    if (!playing || !engine) return
    if (done) { setPlaying(false); return }
    const id = window.setInterval(() => step(speed), 140)
    return () => window.clearInterval(id)
  }, [playing, engine, speed, step, done])

  useEffect(() => {
    if (engine && tenant) refresh(engine, tenant)
  }, [engine, tenant, refresh])

  useEffect(() => {
    if (engine && open && tenant) {
      try { setDetail(engine.shipment(tenant, open)) } catch { setDetail(null) }
    } else setDetail(null)
  }, [engine, open, tenant, dash])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement
      if (open || ['INPUT', 'TEXTAREA', 'SELECT'].includes(el.tagName)) return
      if (e.code === 'Space') { e.preventDefault(); setPlaying((p) => !p) }
      if (e.code === 'ArrowRight') { e.preventDefault(); step(1) }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, step])

  const k = dash?.kpis
  const det = dash?.detection
  const tenants = meta?.tenants ?? []
  const progressPct = dash ? Math.round((dash.progress[0] / Math.max(1, dash.progress[1])) * 100) : 0

  const tabs = useMemo(() => [
    { id: 'invoices' as const, label: 'Invoices', badge: k ? <span className="font-mono text-[10px] text-dim">{k.invoices}</span> : undefined },
    { id: 'carriers' as const, label: 'Carriers' },
    { id: 'ledger' as const, label: 'Ledger' },
    { id: 'policy' as const, label: 'Policy lab' },
    { id: 'replay' as const, label: 'Replay proof' },
    { id: 'how' as const, label: 'How it works' },
  ], [k])

  if (error) {
    return (
      <main className="mx-auto max-w-xl px-4 py-24 text-center">
        <h1 className="text-2xl">The audit engine did not load</h1>
        <p className="mt-2 text-dim">{error}. Build it with <code className="text-white">npm run wasm</code>.</p>
      </main>
    )
  }

  return (
    <main className="mx-auto w-full max-w-[1480px] px-4 pb-16">
      {/* Header */}
      <div className="flex flex-col gap-5 pb-5 pt-7 lg:flex-row lg:items-end lg:justify-between">
        <div className="min-w-0">
          <img src={`${import.meta.env.BASE_URL}logo-lockup.svg`} alt="Tollgate by TECHMINES" className="h-11 w-auto sm:h-12" />
          <h1 className="mt-4 max-w-3xl text-2xl font-bold leading-tight sm:text-[2rem]">
            Freight invoice audit that checks every carrier charge against the contract <span className="text-red-500">and what the trucks actually did.</span>
          </h1>
          <p className="mt-2 max-w-3xl text-sm text-dim">
            Multi-tenant, event-driven: Redpanda in, Postgres with row-level security underneath, a double-entry ledger out. Everything
            on this page is simulated, and the audit engine is the Go backend running in your browser as WebAssembly.
          </p>
        </div>
        <div className="shrink-0">
          <Label className="mb-1.5">Tenant · isolated by row-level security</Label>
          <div className="flex border border-line" role="radiogroup" aria-label="Tenant">
            {tenants.map((t) => (
              <button key={t.id} role="radio" aria-checked={tenant === t.id} onClick={() => setTenant(t.id)}
                className={`px-3 py-2 text-xs font-semibold uppercase transition-colors sm:px-4 ${tenant === t.id ? 'bg-white text-black' : 'text-dim hover:text-white'}`}>
                {t.name}
              </button>
            ))}
          </div>
        </div>
      </div>

      {/* Controls */}
      <div className="sticky top-11 z-40 -mx-4 border-y border-line bg-black/85 px-4 py-2.5 backdrop-blur-md">
        <div className="flex flex-wrap items-center gap-2">
          <button className="tm-btn tm-btn--solid !px-4 !py-2" onClick={() => setPlaying((p) => !p)} disabled={!engine || done} aria-label={playing ? 'Pause' : 'Play'}>
            {playing ? <Pause size={15} /> : <Play size={15} />} {playing ? 'Pause' : 'Play'}
          </button>
          <button className="tm-btn !px-3 !py-2" onClick={() => step(1)} disabled={!engine || done} title="Process one message (→)"><SkipForward size={15} /> Step</button>
          <button className="tm-btn !px-3 !py-2" onClick={() => { setPlaying(false); step(100_000) }} disabled={!engine || done} title="Consume everything"><FastForward size={15} /> To end</button>
          <button className="tm-btn !px-3 !py-2" onClick={() => { setPlaying(false); if (engine) start(engine, cfg) }} disabled={!engine} title="Restart the scenario"><RotateCcw size={15} /></button>
          <div className="flex border border-line" role="radiogroup" aria-label="Speed">
            {SPEEDS.map((s) => (
              <button key={s} role="radio" aria-checked={speed === s} onClick={() => setSpeed(s)}
                className={`px-2.5 py-2 font-mono text-[11px] font-bold ${speed === s ? 'bg-white text-black' : 'text-dim hover:text-white'}`}>{s}×</button>
            ))}
          </div>
          <div className="order-last flex w-full items-center gap-3 sm:order-none sm:ml-2 sm:w-auto sm:flex-1">
            <div className="h-1.5 flex-1 bg-line" aria-label={`${progressPct}% of messages consumed`}>
              <div className="h-full bg-red-500 transition-[width] duration-150" style={{ width: `${progressPct}%` }} />
            </div>
            <span className="shrink-0 font-mono text-[11px] text-dim tabular-nums">{dash?.progress[0] ?? 0}/{dash?.progress[1] ?? 0} msgs</span>
          </div>
          <button className={`tm-btn !px-3 !py-2 ${settings ? '!border-red-500' : ''}`} onClick={() => setSettings((s) => !s)} aria-expanded={settings}>
            <Settings2 size={15} /> Scenario
          </button>
        </div>

        {settings && (
          <div className="mt-3 grid gap-5 border-t border-line pt-4 sm:grid-cols-2 lg:grid-cols-[repeat(4,1fr)_auto]">
            <Slider label="Shipments per tenant" value={draft.shipments_per_tenant} min={6} max={120} step={6} format={String} onChange={(v) => setDraft({ ...draft, shipments_per_tenant: v })} />
            <Slider label="Invoices with a billing error" value={draft.error_rate_pct} min={0} max={80} step={5} format={(v) => `${v}%`} onChange={(v) => setDraft({ ...draft, error_rate_pct: v })} />
            <Slider label="Messages redelivered" value={draft.duplicate_pct} min={0} max={60} step={2} format={(v) => `${v}%`} onChange={(v) => setDraft({ ...draft, duplicate_pct: v })} />
            <Slider label="Telematics arriving after the invoice" value={draft.late_telematics_pct} min={0} max={80} step={5} format={(v) => `${v}%`} onChange={(v) => setDraft({ ...draft, late_telematics_pct: v })} />
            <div className="flex flex-col justify-between gap-3">
              <Toggle label="Fully shuffled delivery" checked={draft.shuffled} onChange={(v) => setDraft({ ...draft, shuffled: v })} />
              <div className="flex items-center gap-2">
                <label className="flex items-center gap-2 font-mono text-[11px] text-dim">
                  seed
                  <input type="number" min={1} value={draft.seed} onChange={(e) => setDraft({ ...draft, seed: Math.max(1, Number(e.target.value) || 1) })}
                    className="w-16 border border-line bg-black px-2 py-1 text-white" />
                </label>
                <button className="tm-btn tm-btn--solid !px-3 !py-1.5 !text-xs" onClick={() => { setPlaying(false); setCfg(draft); if (engine) start(engine, draft); setSettings(false) }}>Apply</button>
              </div>
            </div>
          </div>
        )}
      </div>

      {!dash || !k || !det ? (
        <p className="py-24 text-center font-mono text-sm text-dim">Loading the audit engine…</p>
      ) : (
        <>
          {/* KPIs */}
          <div className="mt-5 grid grid-cols-2 gap-3 md:grid-cols-3 xl:grid-cols-6">
            <Kpi label="Billed · audited" value={money0(k.billed_cents)} sub={`${k.audited} invoices · ${k.held} on hold`} />
            <Kpi label="Approved to pay" value={money0(k.approved_cents)} sub={`${dash.decisions.APPROVE ?? 0} clean · ${dash.decisions.APPROVE_VARIANCE ?? 0} with variance`} />
            <Kpi label="Disputed" tone="red" value={money0(k.disputed_cents)} sub={`${dash.decisions.SHORT_PAY ?? 0} short-paid · ${dash.decisions.REJECT ?? 0} rejected`} hint="Withheld from payment and claimed back from carriers" />
            <Kpi label="Expected recovery" value={money0(k.expected_recovery_cents)} sub="disputed × assumed win rate" />
            <Kpi label="Overcharge paid knowingly" tone="dim" value={money0(k.leakage_cents)} sub="cheaper to pay than to dispute" />
            <Kpi label="Open accrual" value={money0(k.accrued_cents)} sub={`${k.delivered}/${k.shipments} delivered · ${k.re_audits} verdict changes`} hint="Delivered freight not yet invoiced, booked to 2150 Accrued freight" />
          </div>

          {/* Answer key */}
          <div className="mt-3 flex flex-wrap items-center gap-x-5 gap-y-2 border border-line bg-panel/60 px-4 py-2.5 text-xs">
            <Label className="!text-[10px]">Against the simulator's answer key</Label>
            <span className="font-mono text-dim">injected errors <span className="text-white">{det.injected}</span></span>
            <span className="font-mono text-dim">caught <span className={det.caught === det.injected ? 'text-green-500' : 'text-red-500'}>{det.caught}</span></span>
            <span className="font-mono text-dim">clean invoices flagged <span className={det.clean_flagged === 0 ? 'text-green-500' : 'text-red-500'}>{det.clean_flagged}</span> of {det.clean}</span>
            {det.pending > 0 && <span className="font-mono text-dim">on hold <span className="text-white">{det.pending}</span></span>}
            <div className="sm:ml-auto"><Toggle label="Show answer key in the queue" checked={showKey} onChange={setShowKey} /></div>
          </div>

          {/* Main */}
          <div className="mt-5 grid gap-5 xl:grid-cols-[minmax(320px,380px)_1fr]">
            <div className="xl:sticky xl:top-[7.5rem] xl:h-[calc(100vh-9rem)]">
              <Stream rows={dash.stream ?? []} onOpen={setOpen} />
            </div>
            <section className="min-w-0 border border-line bg-panel/75 backdrop-blur-md">
              <Tabs tabs={tabs} value={tab} onChange={setTab} />
              {tab === 'invoices' && <InvoiceQueue rows={dash.invoices ?? []} counts={dash.decisions} onOpen={setOpen} showKey={showKey} tenant={tenant} />}
              {tab === 'carriers' && <Carriers d={dash} />}
              {tab === 'ledger' && <Ledger d={dash} />}
              {tab === 'policy' && engine && <PolicyLab key={tenant} engine={engine} cfg={cfg} d={dash} />}
              {tab === 'replay' && engine && <ReplayProof engine={engine} cfg={cfg} />}
              {tab === 'how' && <HowItWorks />}
            </section>
          </div>
          <p className="mt-4 font-mono text-[11px] text-stroke">Space play/pause · → step one message · Esc close a shipment</p>
        </>
      )}

      <ShipmentDrawer detail={detail} onClose={() => setOpen(null)} disputeCost={dash?.policy.dispute_cost_cents ?? 0} />
    </main>
  )
}
