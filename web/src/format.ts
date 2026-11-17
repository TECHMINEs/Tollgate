import type { Decision, Finding } from './types'

const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD' })
const usd0 = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 })

export const money = (cents: number) => usd.format(cents / 100)
export const money0 = (cents: number) => usd0.format(cents / 100)

export const time = (iso: string) =>
  new Date(iso).toLocaleString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' })
export const clock = (iso: string) =>
  new Date(iso).toLocaleTimeString('en-US', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: 'UTC' })

export const pretty = (id: string) => id.split('-').map((w) => w[0].toUpperCase() + w.slice(1)).join(' ')

export const decisionLabel: Record<Decision, string> = {
  APPROVE: 'Approve',
  APPROVE_VARIANCE: 'Pay variance',
  SHORT_PAY: 'Short-pay',
  REJECT: 'Reject',
  HOLD: 'Hold',
}

export const decisionHelp: Record<Decision, string> = {
  APPROVE: 'Every line matches the contract and the evidence. Pay as billed.',
  APPROVE_VARIANCE: 'Overcharged, but the expected recovery is below the cost of working a dispute. Pay and record the leakage.',
  SHORT_PAY: 'Pay the audited amount and dispute the difference with the carrier.',
  REJECT: 'Duplicate of an invoice already received. Pay nothing.',
  HOLD: 'Evidence or the tariff has not arrived yet. Re-audited automatically when it does.',
}

export const findingLabel: Record<Finding, string> = {
  MATCH: 'Match',
  OVERBILLED: 'Overbilled',
  UNDERBILLED: 'Underbilled',
  UNSUPPORTED: 'Unsupported',
  UNCONTRACTED: 'Uncontracted',
  DUPLICATE: 'Duplicate',
  PENDING: 'Pending',
}

export const sourceLabel: Record<string, string> = {
  dock_system: 'Dock system',
  eld_geofence: 'ELD geofence',
  carrier_edi: 'Carrier EDI 214',
  tms: 'TMS tender',
  carrier_billing: 'Carrier EDI 210',
  contracts: 'Contracts',
}

export const typeLabel: Record<string, string> = {
  'tariff.published': 'TARIFF',
  'shipment.tendered': 'TENDER',
  'stop.reported': 'STOP',
  'invoice.received': 'INVOICE',
}

export const chargeLabel: Record<string, string> = {
  LINEHAUL: 'Linehaul',
  FUEL: 'Fuel surcharge',
  DETENTION: 'Detention',
  LIFTGATE: 'Liftgate',
  STOP_OFF: 'Stop-off',
  LUMPER: 'Lumper',
}

export const pct = (bps: number) => `${(bps / 100).toFixed(0)}%`
