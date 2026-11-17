import type { StreamRow } from '../types'
import { sourceLabel, time, typeLabel } from '../format'
import { DecisionChip, Empty, Panel } from './ui'

const typeTone: Record<string, string> = {
  'invoice.received': 'border-white text-white',
  'shipment.tendered': 'border-stroke text-body',
  'stop.reported': 'border-line text-dim',
  'tariff.published': 'border-stroke text-body',
}

export function Stream({ rows, onOpen, partitions = 6 }: { rows: StreamRow[]; onOpen: (shipment: string) => void; partitions?: number }) {
  return (
    <Panel
      kicker={`tollgate.freight.v1 · ${partitions} partitions`}
      title="Broker stream"
      className="flex h-full flex-col"
      bodyClass="flex-1 overflow-y-auto max-h-[70vh] xl:max-h-none"
      actions={<span className="font-mono text-[10px] text-dim">newest first</span>}
    >
      {rows.length === 0 ? (
        <Empty>Press Play. Tariffs, tenders, stop reports and invoices arrive here in broker order, not event-time order.</Empty>
      ) : (
        <ol className="divide-y divide-line">
          {rows.map((r) => (
            <li key={`${r.seq}`} className={`px-3 py-2 text-xs ${r.dropped ? 'opacity-55' : ''}`}>
              <div className="flex items-center gap-2">
                <span className="w-16 shrink-0 font-mono text-[10px] text-dim tabular-nums">p{r.partition}·{r.offset}</span>
                <span className={`border px-1 font-mono text-[9px] font-bold tracking-[0.1em] ${typeTone[r.type] ?? 'border-line text-dim'}`}>
                  {typeLabel[r.type] ?? r.type}
                </span>
                <span className="truncate text-dim">{sourceLabel[r.source] ?? r.source}</span>
                {r.dropped && (
                  <span className="ml-auto shrink-0 border border-stroke px-1 font-mono text-[9px] font-bold text-dim" title="The inbox already holds this event ID; the redelivery changes nothing.">
                    DUPLICATE · DROPPED
                  </span>
                )}
                {r.error && <span className="ml-auto shrink-0 font-mono text-[9px] font-bold text-red-500">DLQ</span>}
              </div>
              <div className="mt-1 flex items-center gap-2 pl-[4.5rem]">
                {r.shipment_id ? (
                  <button onClick={() => onOpen(r.shipment_id)} className="font-mono text-[11px] text-white hover:text-red-500">
                    {r.shipment_id}
                  </button>
                ) : null}
                <span className={`truncate ${r.dropped ? 'line-through' : ''} text-body`}>{r.detail}</span>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5 pl-[4.5rem]">
                <span className="font-mono text-[10px] text-dim">event {time(r.occurred_at)}</span>
                {r.verdicts?.map((v) => (
                  <span key={`${v.invoice_id}-${v.version}`} className="inline-flex items-center gap-1">
                    <span className="font-mono text-[10px] text-dim">
                      {v.invoice_id} v{v.version}
                    </span>
                    <DecisionChip d={v.decision} />
                  </span>
                ))}
                {r.postings > 0 && <span className="font-mono text-[10px] text-dim">+{r.postings} postings</span>}
              </div>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  )
}
