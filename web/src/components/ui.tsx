import type { ReactNode } from 'react'
import type { Decision, Finding } from '../types'
import { decisionHelp, decisionLabel, findingLabel } from '../format'

export function Label({ children, className = '' }: { children: ReactNode; className?: string }) {
  return <p className={`tm-label ${className}`}>{children}</p>
}

export function Panel({ title, kicker, actions, children, className = '', bodyClass = '' }: {
  title?: ReactNode
  kicker?: string
  actions?: ReactNode
  children: ReactNode
  className?: string
  bodyClass?: string
}) {
  return (
    <section className={`border border-line bg-panel/75 backdrop-blur-md min-w-0 ${className}`}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-line px-4 py-3">
          <div className="min-w-0">
            {kicker && <Label className="!text-[10px]">{kicker}</Label>}
            {title && <h2 className="truncate text-base font-semibold">{title}</h2>}
          </div>
          {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
        </header>
      )}
      <div className={bodyClass}>{children}</div>
    </section>
  )
}

const decisionStyle: Record<Decision, string> = {
  APPROVE: 'border-green-500 text-green-500',
  APPROVE_VARIANCE: 'border-stroke text-white',
  SHORT_PAY: 'border-red-500 text-red-500',
  REJECT: 'border-red-500 bg-red-500 text-black',
  HOLD: 'border-dashed border-stroke text-dim',
}

export function DecisionChip({ d, size = 'sm' }: { d: Decision; size?: 'sm' | 'lg' }) {
  return (
    <span
      title={decisionHelp[d]}
      className={`inline-flex items-center whitespace-nowrap border font-mono font-bold uppercase ${
        size === 'lg' ? 'px-3 py-1 text-sm tracking-[0.12em]' : 'px-1.5 py-0.5 text-[10px] tracking-[0.1em]'
      } ${decisionStyle[d]}`}
    >
      {decisionLabel[d]}
    </span>
  )
}

const findingStyle: Record<Finding, string> = {
  MATCH: 'text-dim border-line',
  OVERBILLED: 'text-red-500 border-red-500/60',
  UNDERBILLED: 'text-white border-stroke',
  UNSUPPORTED: 'text-red-500 border-red-500/60',
  UNCONTRACTED: 'text-red-500 border-red-500/60',
  DUPLICATE: 'text-black bg-red-500 border-red-500',
  PENDING: 'text-dim border-dashed border-stroke',
}

export function FindingChip({ f }: { f: Finding }) {
  return (
    <span className={`inline-flex whitespace-nowrap border px-1.5 py-0.5 font-mono text-[10px] font-bold uppercase tracking-[0.08em] ${findingStyle[f]}`}>
      {findingLabel[f]}
    </span>
  )
}

export function Kpi({ label, value, sub, tone = 'plain', hint }: {
  label: string
  value: string
  sub?: ReactNode
  tone?: 'plain' | 'red' | 'green' | 'dim'
  hint?: string
}) {
  const color = { plain: 'text-white', red: 'text-red-500', green: 'text-green-500', dim: 'text-dim' }[tone]
  return (
    <div title={hint} className="relative border border-line bg-panel/75 px-4 py-3 backdrop-blur-md">
      {tone === 'red' && <span className="absolute inset-y-0 left-0 w-[3px] bg-red-500" aria-hidden />}
      <Label className="!text-[10px]">{label}</Label>
      <p className={`mt-1 font-mono text-xl font-bold tabular-nums sm:text-2xl ${color}`}>{value}</p>
      {sub && <p className="mt-0.5 truncate text-xs text-dim">{sub}</p>}
    </div>
  )
}

export function Tabs<T extends string>({ tabs, value, onChange }: {
  tabs: { id: T; label: string; badge?: ReactNode }[]
  value: T
  onChange: (t: T) => void
}) {
  return (
    <div role="tablist" className="flex overflow-x-auto border-b border-line">
      {tabs.map((t) => (
        <button
          key={t.id}
          role="tab"
          aria-selected={value === t.id}
          onClick={() => onChange(t.id)}
          className={`relative flex shrink-0 items-center gap-2 px-4 py-3 text-sm font-semibold uppercase transition-colors ${
            value === t.id ? 'text-white' : 'text-dim hover:text-white'
          }`}
        >
          {t.label}
          {t.badge}
          {value === t.id && <span className="absolute inset-x-0 bottom-0 h-[2px] bg-red-500" aria-hidden />}
        </button>
      ))}
    </div>
  )
}

export function Slider({ label, value, min, max, step = 1, onChange, format }: {
  label: string
  value: number
  min: number
  max: number
  step?: number
  onChange: (v: number) => void
  format: (v: number) => string
}) {
  return (
    <label className="block">
      <span className="flex items-baseline justify-between gap-2">
        <span className="text-sm text-body">{label}</span>
        <span className="font-mono text-sm font-bold text-white tabular-nums">{format(value)}</span>
      </span>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="mt-2 w-full accent-[#FF2626]"
      />
    </label>
  )
}

export function Toggle({ label, checked, onChange, hint }: { label: string; checked: boolean; onChange: (v: boolean) => void; hint?: string }) {
  return (
    <label className="flex cursor-pointer items-start justify-between gap-4">
      <span>
        <span className="block text-sm text-body">{label}</span>
        {hint && <span className="block text-xs text-dim">{hint}</span>}
      </span>
      <button
        type="button"
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className={`relative mt-0.5 h-5 w-9 shrink-0 border transition-colors ${checked ? 'border-red-500 bg-red-500' : 'border-stroke bg-transparent'}`}
      >
        <span className={`absolute top-[2px] h-[14px] w-[14px] transition-all ${checked ? 'left-[18px] bg-black' : 'left-[2px] bg-dim'}`} />
      </button>
    </label>
  )
}

export function Empty({ children }: { children: ReactNode }) {
  return <p className="px-4 py-10 text-center text-sm text-dim">{children}</p>
}
