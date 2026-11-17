import type { Source, StopEvent, StopTimeline, Tariff } from '../types'
import { clock, sourceLabel } from '../format'

const SOURCES: Source[] = ['dock_system', 'eld_geofence', 'carrier_edi']
const W = 640
const LEFT = 104
const ROW = 26
const MIN = 60_000

/** One stop: the contract's clock on top, then what each source reported. */
export function StopChart({ st, events, tariff }: { st: StopTimeline; events: StopEvent[]; tariff?: Tariff }) {
  const appt = new Date(st.stop.appointment).getTime()
  const evs = events.filter((e) => e.stop_seq === st.stop.seq)
  const times = [appt, ...evs.map((e) => new Date(e.at).getTime())]
  const t0 = Math.min(...times) - 25 * MIN
  const t1 = Math.max(...times, appt + 3 * 60 * MIN) + 25 * MIN
  const x = (t: number) => LEFT + ((t - t0) / (t1 - t0)) * (W - LEFT - 12)

  const rows = SOURCES.filter((s) => evs.some((e) => e.source === s))
  const H = 58 + rows.length * ROW + 22

  // The contract clock, from the chosen observations.
  const arr = st.arrival ? new Date(st.arrival.at).getTime() : null
  const dep = st.departure ? new Date(st.departure.at).getTime() : null
  const det = tariff?.detention
  let clockStart: number | null = null
  let freeEnd: number | null = null
  let forfeited = false
  if (arr && det) {
    forfeited = (arr - appt) / MIN > det.late_grace_minutes
    clockStart = Math.max(arr, appt)
    freeEnd = clockStart + det.free_minutes * MIN
  }

  const ticks: number[] = []
  for (let t = Math.ceil(t0 / (60 * MIN)) * 60 * MIN; t < t1; t += 60 * MIN) ticks.push(t)

  return (
    <svg viewBox={`0 0 ${W} ${H}`} className="w-full" role="img" aria-label={`Stop ${st.stop.seq} timeline`}>
      <defs>
        <pattern id={`hatch-${st.stop.seq}`} width="6" height="6" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <rect width="3" height="6" fill="#FF2626" opacity="0.55" />
        </pattern>
      </defs>
      {ticks.map((t) => (
        <g key={t}>
          <line x1={x(t)} x2={x(t)} y1={14} y2={H - 18} stroke="#2E2E2E" />
          <text x={x(t)} y={H - 6} fill="#A9A9A9" fontSize="9" textAnchor="middle" fontFamily="JetBrains Mono">
            {clock(new Date(t).toISOString())}
          </text>
        </g>
      ))}

      {/* contract row */}
      <text x={0} y={34} fill="#FFFFFF" fontSize="10" fontFamily="JetBrains Mono" fontWeight="700">CONTRACT</text>
      {clockStart && freeEnd && !forfeited && (
        <>
          <rect x={x(clockStart)} y={24} width={Math.max(0, x(Math.min(freeEnd, dep ?? freeEnd)) - x(clockStart))} height={14} fill="#4D4D4D" opacity="0.6" />
          {dep && dep > freeEnd && (
            <rect x={x(freeEnd)} y={24} width={x(dep) - x(freeEnd)} height={14} fill="#FF2626" />
          )}
        </>
      )}
      {forfeited && arr && (
        <rect x={x(appt + (det?.late_grace_minutes ?? 0) * MIN)} y={24} width={Math.max(2, x(arr) - x(appt + (det?.late_grace_minutes ?? 0) * MIN))} height={14} fill={`url(#hatch-${st.stop.seq})`} />
      )}
      <line x1={x(appt)} x2={x(appt)} y1={16} y2={H - 18} stroke="#FFFFFF" strokeDasharray="3 3" />
      <text x={x(appt) + 4} y={16} fill="#FFFFFF" fontSize="9" fontFamily="JetBrains Mono">APPT {clock(st.stop.appointment)}</text>

      {/* source rows */}
      {rows.map((src, i) => {
        const y = 58 + i * ROW
        const mine = evs.filter((e) => e.source === src)
        const a = mine.filter((e) => e.kind === 'arrived').map((e) => new Date(e.at).getTime())
        const d = mine.filter((e) => e.kind === 'departed').map((e) => new Date(e.at).getTime())
        const from = a.length ? Math.min(...a) : null
        const to = d.length ? Math.max(...d) : null
        const chosen = st.arrival?.source === src || st.departure?.source === src
        const conflict = st.conflicts?.some((c) => c.source === src)
        const stroke = conflict ? '#FF2626' : chosen ? '#FFFFFF' : '#4D4D4D'
        return (
          <g key={src}>
            <text x={0} y={y + 4} fill={chosen ? '#FFFFFF' : '#A9A9A9'} fontSize="10" fontFamily="JetBrains Mono">
              {sourceLabel[src].replace(' 214', '').toUpperCase()}
            </text>
            {from !== null && to !== null && (
              <rect x={x(from)} y={y - 5} width={Math.max(2, x(to) - x(from))} height={10}
                fill={chosen ? '#FFFFFF' : 'none'} fillOpacity={chosen ? 0.16 : 0} stroke={stroke} strokeWidth={1.5} />
            )}
            {mine.map((e) => (
              <rect key={e.event_id} x={x(new Date(e.at).getTime()) - 2.5} y={y - 8} width={5} height={16}
                fill={e.used ? '#FFFFFF' : conflict && e.kind === 'departed' ? '#FF2626' : '#4D4D4D'}>
                <title>{`${sourceLabel[e.source]} ${e.kind} ${clock(e.at)}${e.used ? ' (used)' : ''}`}</title>
              </rect>
            ))}
          </g>
        )
      })}
    </svg>
  )
}

export function ChartLegend() {
  const item = (swatch: React.ReactNode, label: string) => (
    <span className="flex items-center gap-1.5">
      {swatch}
      {label}
    </span>
  )
  return (
    <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[10px] text-dim">
      {item(<span className="h-2.5 w-4 bg-stroke/60" />, 'free time')}
      {item(<span className="h-2.5 w-4 bg-red-500" />, 'billable detention')}
      {item(<span className="h-2.5 w-4 border border-dashed border-white" />, 'appointment')}
      {item(<span className="h-3 w-1.5 bg-white" />, 'event used')}
      {item(<span className="h-3 w-1.5 bg-red-500" />, 'conflicting report')}
    </div>
  )
}
