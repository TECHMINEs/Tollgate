import { useEffect, useState } from 'react'
import { X } from 'lucide-react'
import type { ShipmentDetail, Verdict } from '../types'
import { chargeLabel, decisionHelp, money, pct, pretty, sourceLabel, time } from '../format'
import { ChartLegend, StopChart } from './StopChart'
import { DecisionChip, Empty, FindingChip, Label } from './ui'

export function ShipmentDrawer({ detail, onClose, disputeCost }: { detail: ShipmentDetail | null; onClose: () => void; disputeCost: number }) {
  const ids = detail ? Object.keys(detail.verdicts).sort() : []
  const [invoice, setInvoice] = useState<string | null>(null)
  const [version, setVersion] = useState<number | null>(null)

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  useEffect(() => {
    setInvoice(null)
    setVersion(null)
  }, [detail?.shipment.id])

  if (!detail) return null
  const s = detail.shipment
  const activeId = invoice && detail.verdicts[invoice] ? invoice : ids[0]
  const versions = activeId ? detail.verdicts[activeId] : []
  const v: Verdict | undefined = versions.find((x) => x.version === version) ?? versions[versions.length - 1]
  const events = detail.events ?? []

  return (
    <div className="fixed inset-0 z-[70] flex justify-end" role="dialog" aria-modal="true" aria-label={`Shipment ${s.id}`}>
      <button className="absolute inset-0 bg-black/70 backdrop-blur-sm" onClick={onClose} aria-label="Close" />
      <aside className="relative flex h-full w-full max-w-[820px] flex-col border-l border-line bg-[#050505]">
        <header className="flex items-start justify-between gap-4 border-b border-line px-5 py-4">
          <div className="min-w-0">
            <Label>Shipment · {pretty(s.carrier_id)}</Label>
            <h2 className="font-mono text-xl font-bold">{s.id}</h2>
            <p className="mt-1 text-sm text-dim">
              {detail.lane ? `${detail.lane.origin} → ${detail.lane.destination}` : s.lane_id} · {s.miles} mi · {s.stops.length} stops · tariff {s.tariff_id} v{s.tariff_version}
              {s.liftgate_requested ? ' · liftgate requested' : ''}
            </p>
          </div>
          <button onClick={onClose} className="tm-btn !p-2" aria-label="Close shipment">
            <X size={16} />
          </button>
        </header>

        <div className="flex-1 space-y-6 overflow-y-auto px-5 py-5">
          {/* Evidence */}
          <section>
            <div className="mb-3 flex flex-wrap items-end justify-between gap-2">
              <div>
                <Label>Evidence</Label>
                <h3 className="text-lg">What the trucks actually did</h3>
              </div>
              <ChartLegend />
            </div>
            <div className="space-y-3">
              {detail.timeline.stops.map((st) => (
                <div key={st.stop.seq} className="border border-line bg-panel/60 p-3">
                  <div className="mb-1 flex flex-wrap items-baseline justify-between gap-2">
                    <p className="text-sm text-white">
                      <span className="font-mono text-dim">STOP {st.stop.seq}</span> · {st.stop.facility}{' '}
                      <span className="text-dim">({st.stop.type})</span>
                    </p>
                    <p className="font-mono text-[11px] text-dim">
                      {st.arrival ? `in ${time(st.arrival.at)} (${sourceLabel[st.arrival.source]})` : 'no arrival yet'} ·{' '}
                      {st.departure ? `out ${time(st.departure.at)} (${sourceLabel[st.departure.source]})` : 'no departure yet'}
                    </p>
                  </div>
                  <StopChart st={st} events={events} tariff={detail.tariff} />
                  {st.conflicts?.map((c, i) => (
                    <p key={i} className="mt-1 font-mono text-[11px] text-red-500">
                      {sourceLabel[c.source]} reports {c.kind} {c.delta_minutes > 0 ? '+' : ''}
                      {c.delta_minutes} min from the trusted source
                    </p>
                  ))}
                  {st.anomaly && <p className="mt-1 font-mono text-[11px] text-red-500">{st.anomaly}</p>}
                </div>
              ))}
            </div>
            <p className="mt-2 text-xs text-dim">
              Trust order: dock system, then ELD geofence, then the carrier's own EDI 214. Within a source the first arrival and the last
              departure count, so yard moves and duplicate pings do not change the result.
            </p>
          </section>

          {/* Audit */}
          <section>
            <div className="mb-3 flex flex-wrap items-end justify-between gap-3">
              <div>
                <Label>Audit</Label>
                <h3 className="text-lg">Invoice against contract and evidence</h3>
              </div>
              {ids.length > 1 && (
                <div className="flex gap-1">
                  {ids.map((id) => (
                    <button key={id} onClick={() => { setInvoice(id); setVersion(null) }}
                      className={`border px-2 py-1 font-mono text-[11px] ${id === activeId ? 'border-red-500 text-white' : 'border-line text-dim'}`}>
                      {id}
                    </button>
                  ))}
                </div>
              )}
            </div>
            {!v ? (
              <Empty>No invoice received for this shipment yet. The expected cost is accrued at delivery: {money(detail.expected_cents)}.</Empty>
            ) : (
              <div className="border border-line">
                <div className="flex flex-wrap items-center gap-3 border-b border-line bg-panel/70 px-4 py-3">
                  <DecisionChip d={v.decision} size="lg" />
                  <p className="min-w-0 flex-1 text-sm text-body">{decisionHelp[v.decision]}</p>
                  <span className="font-mono text-[11px] text-dim">{v.invoice_number}</span>
                </div>
                <div className="overflow-x-auto">
                  <table className="w-full min-w-[620px] text-xs">
                    <thead>
                      <tr className="border-b border-line text-left">
                        {['Charge', 'Billed', 'Expected', 'Variance', 'Finding', 'Working'].map((h, i) => (
                          <th key={h} className={`tm-label !text-[10px] px-3 py-2 ${i >= 1 && i <= 3 ? 'text-right' : ''}`}>{h}</th>
                        ))}
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-line">
                      {v.lines.map((l) => (
                        <tr key={l.line_no}>
                          <td className="px-3 py-2 align-top text-white">
                            {chargeLabel[l.code] ?? l.code}
                            {l.stop_seq ? <span className="text-dim"> · stop {l.stop_seq}</span> : null}
                          </td>
                          <td className="px-3 py-2 text-right align-top font-mono tabular-nums">{money(l.billed_cents)}</td>
                          <td className="px-3 py-2 text-right align-top font-mono tabular-nums">{money(l.expected_cents)}</td>
                          <td className={`px-3 py-2 text-right align-top font-mono tabular-nums ${l.variance_cents > 0 ? 'text-red-500' : 'text-stroke'}`}>
                            {l.variance_cents === 0 ? '—' : money(l.variance_cents)}
                          </td>
                          <td className="px-3 py-2 align-top"><FindingChip f={l.finding} /></td>
                          <td className="px-3 py-2 align-top text-dim">
                            {l.reason}
                            {l.win_bps ? <span className="text-stroke"> · win {pct(l.win_bps)}</span> : null}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                <dl className="grid grid-cols-2 gap-px border-t border-line bg-line sm:grid-cols-4">
                  {[
                    ['Billed', money(v.billed_cents), 'text-white'],
                    ['Pay now', money(v.approved_cents), 'text-white'],
                    ['Disputed', money(v.disputed_cents), v.disputed_cents ? 'text-red-500' : 'text-stroke'],
                    ['Expected recovery', money(v.expected_recovery_cents), 'text-white'],
                  ].map(([k, val, c]) => (
                    <div key={k} className="bg-[#050505] px-4 py-3">
                      <dt className="tm-label !text-[10px]">{k}</dt>
                      <dd className={`font-mono text-base font-bold tabular-nums ${c}`}>{val}</dd>
                    </div>
                  ))}
                </dl>
                {v.reasons && v.reasons.length > 0 && (
                  <p className="border-t border-line px-4 py-2 text-xs text-dim">
                    {v.reasons.join(' · ')} {v.decision !== 'HOLD' && v.decision !== 'REJECT' ? `(dispute cost ${money(disputeCost)})` : ''}
                  </p>
                )}
              </div>
            )}

            {versions.length > 0 && (
              <div className="mt-3">
                <Label className="mb-2">Verdict history · append-only</Label>
                <ol className="flex flex-wrap gap-2">
                  {versions.map((x) => (
                    <li key={x.version}>
                      <button onClick={() => setVersion(x.version)}
                        className={`flex items-center gap-2 border px-2 py-1.5 ${x.version === v?.version ? 'border-red-500' : 'border-line hover:border-stroke'}`}>
                        <span className="font-mono text-[11px] text-white">v{x.version}</span>
                        <DecisionChip d={x.decision} />
                        <span className="font-mono text-[10px] text-dim" title="Evidence hash: fingerprint of every input to this verdict">#{x.evidence_hash.slice(0, 8)}</span>
                      </button>
                    </li>
                  ))}
                </ol>
                {versions.length > 1 && (
                  <p className="mt-2 text-xs text-dim">
                    A new version is written whenever the evidence behind the verdict changes. When the decision or amounts change too, the previous
                    journal entry is reversed and the new one booked; otherwise the ledger is left alone.
                  </p>
                )}
              </div>
            )}
          </section>

          {/* Raw events */}
          <section>
            <Label className="mb-2">Stop events received · {events.length}</Label>
            <div className="max-h-64 overflow-y-auto border border-line">
              <table className="w-full text-[11px]">
                <tbody className="divide-y divide-line font-mono">
                  {events.map((e) => (
                    <tr key={e.event_id} className={e.used ? 'text-white' : 'text-dim'}>
                      <td className="px-3 py-1.5">stop {e.stop_seq}</td>
                      <td className="px-3 py-1.5">{e.kind}</td>
                      <td className="px-3 py-1.5">{time(e.at)}</td>
                      <td className="px-3 py-1.5">{sourceLabel[e.source]}</td>
                      <td className="px-3 py-1.5 text-right">{e.used ? 'USED' : ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
        </div>
      </aside>
    </div>
  )
}
