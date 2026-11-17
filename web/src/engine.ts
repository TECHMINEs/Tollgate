// Loads the Go engine (compiled to WebAssembly) and wraps its JSON API.
import type { Dashboard, Meta, Policy, ReplayRun, ShipmentDetail, SimConfig, StreamRow, Totals } from './types'

interface Raw {
  start(cfg: string): string
  step(n: number): string
  dashboard(tenant: string, streamLimit: number): string
  shipment(tenant: string, id: string): string
  whatIf(cfg: string, tenant: string, policy: string): string
  replayCheck(cfg: string): string
}

declare global {
  interface Window {
    tollgate?: Raw
    Go?: new () => { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> }
  }
}

const base = import.meta.env.BASE_URL

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const s = document.createElement('script')
    s.src = src
    s.onload = () => resolve()
    s.onerror = () => reject(new Error(`could not load ${src}`))
    document.head.appendChild(s)
  })
}

let ready: Promise<Raw> | null = null

export function loadEngine(): Promise<Raw> {
  ready ??= (async () => {
    if (!window.Go) await loadScript(`${base}wasm_exec.js`)
    const go = new window.Go!()
    const resp = await fetch(`${base}tollgate.wasm`)
    if (!resp.ok) throw new Error(`tollgate.wasm: HTTP ${resp.status}`)
    const { instance } = await WebAssembly.instantiate(await resp.arrayBuffer(), go.importObject)
    const started = new Promise<void>((r) => window.addEventListener('tollgate-ready', () => r(), { once: true }))
    void go.run(instance)
    await started
    return window.tollgate!
  })()
  return ready
}

function parse<T>(s: string): T {
  const v = JSON.parse(s)
  if (v && typeof v === 'object' && 'error' in v && Object.keys(v).length === 1) throw new Error(v.error)
  return v as T
}

export class Engine {
  private raw: Raw
  constructor(raw: Raw) {
    this.raw = raw
  }
  start = (cfg: SimConfig) => parse<Meta>(this.raw.start(JSON.stringify(cfg)))
  step = (n: number) => parse<StreamRow[] | null>(this.raw.step(n)) ?? []
  dashboard = (tenant: string, limit = 80) => parse<Dashboard>(this.raw.dashboard(tenant, limit))
  shipment = (tenant: string, id: string) => parse<ShipmentDetail>(this.raw.shipment(tenant, id))
  whatIf = (cfg: SimConfig, tenant: string, p: Policy) => parse<Totals>(this.raw.whatIf(JSON.stringify(cfg), tenant, JSON.stringify(p)))
  replayCheck = (cfg: SimConfig) => parse<ReplayRun[]>(this.raw.replayCheck(JSON.stringify(cfg)))
}
