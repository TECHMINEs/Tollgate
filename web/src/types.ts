// Mirrors of the Go JSON shapes returned by the WebAssembly engine (internal/demo).

export type Decision = 'APPROVE' | 'APPROVE_VARIANCE' | 'SHORT_PAY' | 'REJECT' | 'HOLD'
export type Finding = 'MATCH' | 'OVERBILLED' | 'UNDERBILLED' | 'UNSUPPORTED' | 'UNCONTRACTED' | 'DUPLICATE' | 'PENDING'
export type Source = 'dock_system' | 'eld_geofence' | 'carrier_edi'

export interface Policy {
  version: number
  line_tolerance_cents: number
  dispute_cost_cents: number
  require_independent_detention: boolean
  conflict_tolerance_minutes: number
  win_bps: Partial<Record<Finding, number>>
  win_bps_carrier_only: number
}

export interface SimConfig {
  seed: number
  shipments_per_tenant: number
  error_rate_pct: number
  duplicate_pct: number
  late_telematics_pct: number
  shuffled: boolean
  start?: string
}

export interface Tenant { id: string; name: string; policy: Policy }

export interface Meta {
  config: SimConfig
  tenants: Tenant[]
  messages: number
  invoices: number
  cursor: number
}

export interface VerdictRef { invoice_id: string; version: number; decision: Decision }

export interface StreamRow {
  seq: number
  partition: number
  offset: number
  tenant_id: string
  type: string
  source: string
  shipment_id: string
  event_id: string
  occurred_at: string
  redelivery: boolean
  dropped: boolean
  verdicts?: VerdictRef[]
  postings: number
  outbox: number
  error?: string
  detail: string
}

export interface KPIs {
  invoices: number
  audited: number
  held: number
  billed_cents: number
  approved_cents: number
  disputed_cents: number
  expected_recovery_cents: number
  leakage_cents: number
  accrued_cents: number
  shipments: number
  delivered: number
  re_audits: number
}

export interface InvoiceRow {
  invoice_id: string
  number: string
  shipment_id: string
  carrier_id: string
  decision: Decision
  billed_cents: number
  approved_cents: number
  disputed_cents: number
  leakage_cents: number
  expected_recovery_cents: number
  versions: number
  changes: number
  findings: string[] | null
  reason: string
  injected: string[] | null
}

export interface CarrierRow {
  carrier_id: string
  invoices: number
  clean: number
  accuracy_pct: number
  billed_cents: number
  disputed_cents: number
  leakage_cents: number
  detention_billed_cents: number
}

export interface FindingRow { finding: Finding; lines: number; variance_cents: number }

export interface Posting {
  journal_key: string
  version: number
  line_no: number
  account: string
  debit_cents: number
  credit_cents: number
  memo: string
}

export interface Detection { injected: number; caught: number; clean: number; clean_flagged: number; pending: number }

export interface Dashboard {
  tenant_id: string
  name: string
  kpis: KPIs
  decisions: Partial<Record<Decision, number>>
  invoices: InvoiceRow[] | null
  carriers: CarrierRow[] | null
  findings: FindingRow[] | null
  balances: Record<string, number>
  journal: Posting[] | null
  detection: Detection
  policy: Policy
  stream: StreamRow[] | null
  progress: [number, number]
}

export interface Stop { seq: number; facility: string; type: string; appointment: string }
export interface Observation { at: string; source: Source; event_id: string }
export interface Conflict { kind: string; source: Source; delta_minutes: number }
export interface StopTimeline {
  stop: Stop
  arrival?: Observation
  departure?: Observation
  conflicts?: Conflict[]
  anomaly?: string
}
export interface StopEvent { event_id: string; source: Source; stop_seq: number; kind: 'arrived' | 'departed'; at: string; used: boolean }

export interface DetentionCalc {
  clock_start: string
  clock_end: string
  dwell_minutes: number
  billable_minutes: number
  increments: number
  forfeited: boolean
  late_minutes?: number
  capped: boolean
  amount_cents: number
}

export interface LineResult {
  line_no: number
  code: string
  stop_seq?: number
  billed_cents: number
  expected_cents: number
  variance_cents: number
  finding: Finding
  reason: string
  evidence?: string[]
  win_bps?: number
  detention?: DetentionCalc
}

export interface Verdict {
  invoice_id: string
  invoice_number: string
  invoice_revision: number
  carrier_id: string
  shipment_id: string
  decision: Decision
  lines: LineResult[]
  reasons?: string[]
  billed_cents: number
  expected_cents: number
  approved_cents: number
  disputed_cents: number
  leakage_cents: number
  expected_recovery_cents: number
  evidence_hash: string
  version: number
}

export interface Lane { id: string; origin: string; destination: string; rate_per_mile_cents?: number; flat_cents?: number; minimum_cents: number }

export interface Tariff {
  id: string
  version: number
  carrier_id: string
  detention: { free_minutes: number; rate_per_hour_cents: number; increment_minutes: number; late_grace_minutes: number; cap_per_stop_cents: number }
  liftgate_cents: number
  stop_off_cents: number
  lumper_cap_cents: number
}

export interface Shipment {
  id: string
  carrier_id: string
  tariff_id: string
  tariff_version: number
  lane_id: string
  miles: number
  fuel_price_cpg: number
  liftgate_requested: boolean
  stops: Stop[]
  tendered_at: string
}

export interface ShipmentDetail {
  shipment: Shipment
  lane?: Lane
  tariff?: Tariff
  timeline: { stops: StopTimeline[]; event_ids: string[] | null }
  events: StopEvent[] | null
  verdicts: Record<string, Verdict[]>
  expected_cents: number
  accrued_cents: number
}

export interface Totals { decisions: Partial<Record<Decision, number>>; kpis: KPIs }

export interface ReplayRun { name: string; messages: number; dropped: number; verdict_versions: number; fingerprint: string }
