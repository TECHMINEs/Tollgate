import { useState } from 'react'
import { Check, Play, X } from 'lucide-react'
import type { Dashboard, Policy, ReplayRun, Totals } from '../types'
import { money, money0, pct, pretty } from '../format'
import type { Engine } from '../engine'
import type { SimConfig } from '../types'
import { DecisionChip, Empty, FindingChip, Label, Slider, Toggle } from './ui'

export function Carriers({ d }: { d: Dashboard }) {
  const carriers = d.carriers ?? []
  const findings = d.findings ?? []
  const maxF = Math.max(1, ...findings.map((f) => f.variance_cents))
  return (
    <div className="grid gap-px bg-line lg:grid-cols-[1.4fr_1fr]">
      <div className="bg-[#070707]">
        <div className="px-4 pt-4">
          <Label>Carrier scorecard</Label>
          <p className="mt-1 text-sm text-dim">Billing accuracy is the share of audited invoices approved exactly as billed. It feeds the next rate negotiation.</p>
        </div>
        {carriers.length === 0 ? <Empty>No audited invoices yet.</Empty> : (
          <div className="overflow-x-auto p-4">
            <table className="w-full min-w-[520px] text-sm">
              <thead>
                <tr className="border-b border-line text-left">
                  {['Carrier', 'Accuracy', 'Invoices', 'Billed', 'Disputed', 'Leakage'].map((h, i) => (
                    <th key={h} className={`tm-label !text-[10px] py-2 pr-3 ${i >= 2 ? 'text-right' : ''}`}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {carriers.map((c) => (
                  <tr key={c.carrier_id}>
                    <td className="py-2.5 pr-3 text-white">{pretty(c.carrier_id)}</td>
                    <td className="py-2.5 pr-3">
                      <div className="flex items-center gap-2">
                        <div className="h-2 w-24 bg-line">
                          <div className={`h-full ${c.accuracy_pct >= 80 ? 'bg-white' : 'bg-red-500'}`} style={{ width: `${c.accuracy_pct}%` }} />
                        </div>
                        <span className="font-mono text-xs tabular-nums">{c.accuracy_pct}%</span>
                      </div>
                    </td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums">{c.invoices}</td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums">{money0(c.billed_cents)}</td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums text-red-500">{money0(c.disputed_cents)}</td>
                    <td className="py-2.5 text-right font-mono text-xs tabular-nums text-dim">{money0(c.leakage_cents)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      <div className="bg-[#070707] p-4">
        <Label>Where the overcharges come from</Label>
        <p className="mt-1 text-sm text-dim">Overcharged dollars by finding, across every audited line.</p>
        {findings.length === 0 ? <Empty>No findings yet.</Empty> : (
          <ul className="mt-4 space-y-3">
            {findings.map((f) => (
              <li key={f.finding}>
                <div className="flex items-center justify-between gap-2 text-xs">
                  <span className="flex items-center gap-2"><FindingChip f={f.finding} /> <span className="text-dim">{f.lines} lines</span></span>
                  <span className="font-mono tabular-nums text-white">{money(f.variance_cents)}</span>
                </div>
                <div className="mt-1.5 h-1.5 bg-line">
                  <div className={`h-full ${f.finding === 'UNDERBILLED' || f.finding === 'PENDING' ? 'bg-stroke' : 'bg-red-500'}`} style={{ width: `${(f.variance_cents / maxF) * 100}%` }} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

const ACCOUNTS: [string, string][] = [
  ['6100_FREIGHT_EXPENSE', 'Freight expense'],
  ['1450_FREIGHT_DISPUTES', 'Freight disputes receivable'],
  ['2150_ACCRUED_FREIGHT', 'Accrued freight (delivered, not invoiced)'],
  ['2010_AP_CARRIERS', 'Accounts payable: carriers'],
  ['2015_AP_DISPUTED', 'Accounts payable: disputed'],
]

export function Ledger({ d }: { d: Dashboard }) {
  const bal = d.balances ?? {}
  const total = Object.values(bal).reduce((a, b) => a + b, 0)
  return (
    <div className="grid gap-px bg-line lg:grid-cols-[1fr_1.3fr]">
      <div className="bg-[#070707] p-4">
        <Label>Trial balance · freight sub-ledger</Label>
        <table className="mt-3 w-full text-sm">
          <thead>
            <tr className="border-b border-line text-left">
              <th className="tm-label !text-[10px] py-2">Account</th>
              <th className="tm-label !text-[10px] py-2 text-right">Debit</th>
              <th className="tm-label !text-[10px] py-2 text-right">Credit</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {ACCOUNTS.map(([id, name]) => {
              const b = bal[id] ?? 0
              return (
                <tr key={id}>
                  <td className="py-2 pr-2">
                    <p className="text-white">{name}</p>
                    <p className="font-mono text-[10px] text-dim">{id.split('_')[0]}</p>
                  </td>
                  <td className="py-2 text-right font-mono text-xs tabular-nums">{b > 0 ? money(b) : ''}</td>
                  <td className="py-2 text-right font-mono text-xs tabular-nums">{b < 0 ? money(-b) : ''}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
        <p className={`mt-3 flex items-center gap-2 font-mono text-xs ${total === 0 ? 'text-green-500' : 'text-red-500'}`}>
          {total === 0 ? <Check size={14} /> : <X size={14} />} debits − credits = {money(total)}
        </p>
        <p className="mt-2 text-xs text-dim">
          Postgres enforces this: a deferred constraint trigger rejects any journal version that does not balance, and postings can never be updated or deleted.
        </p>
      </div>
      <div className="bg-[#070707] p-4">
        <Label>Journal · newest first</Label>
        {(d.journal ?? []).length === 0 ? <Empty>Postings appear when shipments deliver (accruals) and invoices are decided.</Empty> : (
          <ol className="mt-3 max-h-[440px] divide-y divide-line overflow-y-auto border border-line">
            {(d.journal ?? []).map((p) => {
              const reversal = p.memo.startsWith('reverse')
              return (
                <li key={`${p.journal_key}-${p.version}-${p.line_no}`} className="grid grid-cols-[1fr_auto] gap-2 px-3 py-2 text-xs">
                  <div className="min-w-0">
                    <p className={`truncate font-mono ${reversal ? 'text-dim' : 'text-white'}`}>
                      {p.journal_key} <span className="text-stroke">v{p.version}</span>
                    </p>
                    <p className="truncate text-dim">{p.account.replace(/^\d+_/, '').replaceAll('_', ' ').toLowerCase()} · {p.memo}</p>
                  </div>
                  <p className={`text-right font-mono tabular-nums ${reversal ? 'text-dim' : 'text-white'}`}>
                    {p.debit_cents ? `Dr ${money(p.debit_cents)}` : `Cr ${money(p.credit_cents)}`}
                  </p>
                </li>
              )
            })}
          </ol>
        )}
      </div>
    </div>
  )
}

export function PolicyLab({ engine, cfg, d }: { engine: Engine; cfg: SimConfig; d: Dashboard }) {
  const [p, setP] = useState<Policy>(d.policy)
  const [result, setResult] = useState<{ base: Totals; next: Totals } | null>(null)
  const [busy, setBusy] = useState(false)

  const run = () => {
    setBusy(true)
    setTimeout(() => {
      const base = engine.whatIf(cfg, d.tenant_id, d.policy)
      const next = engine.whatIf(cfg, d.tenant_id, { ...p, version: d.policy.version + 1 })
      setResult({ base, next })
      setBusy(false)
    }, 20)
  }

  const rows: [string, (t: Totals) => number, boolean][] = [
    ['Approved to pay', (t) => t.kpis.approved_cents, false],
    ['Disputed', (t) => t.kpis.disputed_cents, false],
    ['Expected recovery', (t) => t.kpis.expected_recovery_cents, false],
    ['Overcharge paid knowingly', (t) => t.kpis.leakage_cents, true],
    ['Short-paid invoices', (t) => t.decisions.SHORT_PAY ?? 0, false],
  ]

  return (
    <div className="grid gap-px bg-line lg:grid-cols-[1fr_1.2fr]">
      <div className="space-y-5 bg-[#070707] p-4">
        <div>
          <Label>Policy lab · {d.name}</Label>
          <p className="mt-1 text-sm text-dim">
            Change the audit policy and re-run the whole scenario through the engine before rolling it out. Win rates are this tenant's assumptions, not measured figures.
          </p>
        </div>
        <Slider label="Cost to work one dispute" value={p.dispute_cost_cents} min={0} max={20000} step={500} format={money0}
          onChange={(v) => setP({ ...p, dispute_cost_cents: v })} />
        <Slider label="Per-line tolerance" value={p.line_tolerance_cents} min={0} max={5000} step={100} format={money0}
          onChange={(v) => setP({ ...p, line_tolerance_cents: v })} />
        <Slider label="Win rate when only the carrier's data disagrees" value={p.win_bps_carrier_only} min={0} max={10000} step={500} format={pct}
          onChange={(v) => setP({ ...p, win_bps_carrier_only: v })} />
        <Slider label="Win rate for overbilled lines" value={p.win_bps.OVERBILLED ?? 0} min={0} max={10000} step={500} format={pct}
          onChange={(v) => setP({ ...p, win_bps: { ...p.win_bps, OVERBILLED: v } })} />
        <Toggle label="Detention needs dock or ELD evidence" hint="Off: the carrier's own timestamps are accepted" checked={p.require_independent_detention}
          onChange={(v) => setP({ ...p, require_independent_detention: v })} />
        <div className="flex gap-2">
          <button className="tm-btn tm-btn--solid" onClick={run} disabled={busy}><Play size={14} /> {busy ? 'Running' : 'Run what-if'}</button>
          <button className="tm-btn" onClick={() => { setP(d.policy); setResult(null) }}>Reset</button>
        </div>
      </div>
      <div className="bg-[#070707] p-4">
        <Label>Current policy vs proposed · full scenario</Label>
        {!result ? <Empty>Adjust the policy and run it. Both columns are computed by the same engine on the same {cfg.shipments_per_tenant} shipments.</Empty> : (
          <>
            <table className="mt-3 w-full text-sm">
              <thead>
                <tr className="border-b border-line text-left">
                  <th className="tm-label !text-[10px] py-2" />
                  <th className="tm-label !text-[10px] py-2 text-right">Current</th>
                  <th className="tm-label !text-[10px] py-2 text-right">Proposed</th>
                  <th className="tm-label !text-[10px] py-2 text-right">Change</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {rows.map(([label, f, lowerIsBetter]) => {
                  const a = f(result.base), b = f(result.next), diff = b - a
                  const isCount = label.includes('invoices')
                  const fmt = (n: number) => (isCount ? String(n) : money0(n))
                  const good = diff === 0 ? 'text-dim' : (diff < 0) === lowerIsBetter ? 'text-white' : 'text-red-500'
                  return (
                    <tr key={label}>
                      <td className="py-2.5 pr-2 text-body">{label}</td>
                      <td className="py-2.5 text-right font-mono text-xs tabular-nums">{fmt(a)}</td>
                      <td className="py-2.5 text-right font-mono text-xs tabular-nums text-white">{fmt(b)}</td>
                      <td className={`py-2.5 text-right font-mono text-xs tabular-nums ${good}`}>{diff > 0 ? '+' : ''}{fmt(diff)}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
            <div className="mt-4 flex flex-wrap gap-2">
              {(['APPROVE', 'APPROVE_VARIANCE', 'SHORT_PAY', 'REJECT', 'HOLD'] as const).map((k) => (
                <span key={k} className="flex items-center gap-1.5 border border-line px-2 py-1">
                  <DecisionChip d={k} />
                  <span className="font-mono text-xs text-dim">{result.base.decisions[k] ?? 0} → <span className="text-white">{result.next.decisions[k] ?? 0}</span></span>
                </span>
              ))}
            </div>
          </>
        )}
      </div>
    </div>
  )
}

export function ReplayProof({ engine, cfg }: { engine: Engine; cfg: SimConfig }) {
  const [runs, setRuns] = useState<ReplayRun[] | null>(null)
  const [busy, setBusy] = useState(false)
  const same = runs && runs.every((r) => r.fingerprint === runs[0].fingerprint)
  return (
    <div className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="max-w-2xl">
          <Label>Replay proof</Label>
          <h3 className="mt-1 text-lg">Same documents, any order, any number of redeliveries: same money</h3>
          <p className="mt-1 text-sm text-dim">
            Runs this scenario four times through fresh pipelines: in broker order, fully shuffled, and with heavy redelivery. Each run is
            fingerprinted over every final verdict's evidence hash and every ledger balance.
          </p>
        </div>
        <button className="tm-btn tm-btn--solid" disabled={busy} onClick={() => { setBusy(true); setTimeout(() => { setRuns(engine.replayCheck(cfg)); setBusy(false) }, 20) }}>
          <Play size={14} /> {busy ? 'Running' : 'Run 4 delivery orders'}
        </button>
      </div>
      {runs && (
        <>
          <div className="mt-4 overflow-x-auto">
            <table className="w-full min-w-[560px] text-sm">
              <thead>
                <tr className="border-b border-line text-left">
                  {['Delivery order', 'Messages', 'Duplicates dropped', 'Verdict versions', 'Final-state fingerprint'].map((h, i) => (
                    <th key={h} className={`tm-label !text-[10px] py-2 pr-3 ${i > 0 && i < 4 ? 'text-right' : ''}`}>{h}</th>
                  ))}
                </tr>
              </thead>
              <tbody className="divide-y divide-line">
                {runs.map((r) => (
                  <tr key={r.name}>
                    <td className="py-2.5 pr-3 text-white">{r.name}</td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums">{r.messages}</td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums">{r.dropped}</td>
                    <td className="py-2.5 pr-3 text-right font-mono text-xs tabular-nums">{r.verdict_versions}</td>
                    <td className="py-2.5 font-mono text-xs text-white">{r.fingerprint}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <p className={`mt-3 flex items-center gap-2 font-mono text-sm ${same ? 'text-green-500' : 'text-red-500'}`}>
            {same ? <Check size={16} /> : <X size={16} />}
            {same ? 'Identical final state in every run.' : 'Fingerprints differ.'}
          </p>
          <p className="mt-1 text-xs text-dim">
            Verdict versions differ between runs on purpose: an invoice that arrives before its telematics is held, then re-audited. The
            intermediate path changes; the outcome does not.
          </p>
        </>
      )}
    </div>
  )
}


