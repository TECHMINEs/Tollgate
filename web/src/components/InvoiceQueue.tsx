import { useMemo, useState } from 'react'
import { Download, Search } from 'lucide-react'
import type { Decision, InvoiceRow } from '../types'
import { decisionHelp, decisionLabel, money, pretty } from '../format'
import { DecisionChip, Empty } from './ui'

const ORDER: Decision[] = ['SHORT_PAY', 'REJECT', 'APPROVE_VARIANCE', 'HOLD', 'APPROVE']

function toCSV(rows: InvoiceRow[]) {
  const head = ['invoice_id', 'carrier_invoice', 'carrier', 'shipment', 'decision', 'billed', 'approved', 'disputed', 'expected_recovery', 'reason']
  const esc = (v: string) => (/[",\n]/.test(v) ? `"${v.replaceAll('"', '""')}"` : v)
  const lines = rows.map((r) =>
    [r.invoice_id, r.number, r.carrier_id, r.shipment_id, r.decision, (r.billed_cents / 100).toFixed(2), (r.approved_cents / 100).toFixed(2),
      (r.disputed_cents / 100).toFixed(2), (r.expected_recovery_cents / 100).toFixed(2), r.reason].map((v) => esc(String(v))).join(','),
  )
  return [head.join(','), ...lines].join('\n')
}

function download(name: string, body: string) {
  const url = URL.createObjectURL(new Blob([body], { type: 'text/csv' }))
  const a = Object.assign(document.createElement('a'), { href: url, download: name })
  a.click()
  URL.revokeObjectURL(url)
}

export function InvoiceQueue({ rows, counts, onOpen, showKey, tenant }: {
  rows: InvoiceRow[]
  counts: Partial<Record<Decision, number>>
  onOpen: (shipment: string) => void
  showKey: boolean
  tenant: string
}) {
  const [filter, setFilter] = useState<Decision | 'ALL' | 'EXCEPTIONS'>('ALL')
  const [q, setQ] = useState('')

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase()
    return rows
      .filter((r) => filter === 'ALL' || (filter === 'EXCEPTIONS' ? r.decision !== 'APPROVE' : r.decision === filter))
      .filter((r) => !needle || [r.invoice_id, r.number, r.shipment_id, r.carrier_id].some((v) => v.toLowerCase().includes(needle)))
      .sort((a, b) => ORDER.indexOf(a.decision) - ORDER.indexOf(b.decision) || b.disputed_cents - a.disputed_cents || a.invoice_id.localeCompare(b.invoice_id))
  }, [rows, filter, q])

  const disputes = rows.filter((r) => r.decision === 'SHORT_PAY' || r.decision === 'REJECT')
  const exceptions = rows.filter((r) => r.decision !== 'APPROVE').length

  const chip = (id: typeof filter, label: string, n: number, help?: string) => (
    <button
      key={id}
      title={help}
      onClick={() => setFilter(id)}
      className={`flex items-center gap-1.5 border px-2.5 py-1.5 font-mono text-[11px] font-bold uppercase tracking-[0.06em] transition-colors ${
        filter === id ? 'border-red-500 bg-red-500 text-black' : 'border-line text-dim hover:border-stroke hover:text-white'
      }`}
    >
      {label}
      <span className={filter === id ? 'text-black/70' : 'text-stroke'}>{n}</span>
    </button>
  )

  return (
    <div>
      <div className="flex flex-col gap-3 border-b border-line p-3 lg:flex-row lg:items-center">
        <div className="flex flex-wrap gap-1.5">
          {chip('ALL', 'All', rows.length)}
          {chip('EXCEPTIONS', 'Exceptions', exceptions, 'Everything that was not approved as billed')}
          {ORDER.map((d) => chip(d, decisionLabel[d], counts[d] ?? 0, decisionHelp[d]))}
        </div>
        <div className="flex gap-2 lg:ml-auto">
          <label className="relative flex-1 lg:w-56 lg:flex-none">
            <Search size={14} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-dim" aria-hidden />
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="Invoice, shipment, carrier"
              aria-label="Search invoices"
              className="w-full border border-line bg-black/60 py-1.5 pl-8 pr-2 font-mono text-xs text-white placeholder:text-stroke"
            />
          </label>
          <button
            className="tm-btn !px-3 !py-1.5 !text-xs"
            disabled={disputes.length === 0}
            onClick={() => download(`${tenant}-disputes.csv`, toCSV(disputes))}
            title="Short-paid and rejected invoices, ready for the carrier dispute workflow"
          >
            <Download size={14} aria-hidden /> Disputes CSV
          </button>
        </div>
      </div>

      {shown.length === 0 ? (
        <Empty>{rows.length === 0 ? 'No invoices yet. Carrier invoices arrive days after delivery.' : 'Nothing matches this filter.'}</Empty>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full min-w-[760px] text-sm">
            <thead>
              <tr className="border-b border-line text-left">
                {['Invoice', 'Carrier', 'Decision', 'Billed', 'Approved', 'Disputed', 'Why'].map((h, i) => (
                  <th key={h} className={`tm-label !text-[10px] px-3 py-2 font-bold ${i >= 3 && i <= 5 ? 'text-right' : ''}`}>{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {shown.map((r) => (
                <tr key={r.invoice_id} onClick={() => onOpen(r.shipment_id)} className="cursor-pointer transition-colors hover:bg-white/[0.04]">
                  <td className="px-3 py-2.5 align-top">
                    <p className="font-mono text-xs text-white">{r.invoice_id}</p>
                    <p className="font-mono text-[10px] text-dim">
                      {r.number} · {r.shipment_id}
                    </p>
                  </td>
                  <td className="px-3 py-2.5 align-top text-xs text-body">{pretty(r.carrier_id)}</td>
                  <td className="px-3 py-2.5 align-top">
                    <DecisionChip d={r.decision} />
                    {r.changes > 0 && (
                      <p className="mt-1 font-mono text-[10px] text-dim" title="Late evidence changed the decision or the amounts; the ledger was rebooked">
                        changed {r.changes}× · v{r.versions}
                      </p>
                    )}
                  </td>
                  <td className="px-3 py-2.5 text-right align-top font-mono text-xs tabular-nums text-body">{money(r.billed_cents)}</td>
                  <td className="px-3 py-2.5 text-right align-top font-mono text-xs tabular-nums text-white">{money(r.approved_cents)}</td>
                  <td className={`px-3 py-2.5 text-right align-top font-mono text-xs tabular-nums ${r.disputed_cents > 0 ? 'text-red-500' : 'text-stroke'}`}>
                    {money(r.disputed_cents)}
                  </td>
                  <td className="max-w-[320px] px-3 py-2.5 align-top text-xs text-dim">
                    <p className="line-clamp-2">{r.decision === 'APPROVE' ? 'All lines match' : r.reason}</p>
                    {showKey && r.injected && r.injected.length > 0 && (
                      <p className="mt-1 font-mono text-[10px] text-stroke">answer key: {r.injected.join(', ').replaceAll('_', ' ')}</p>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
