import { Label } from './ui'

const steps = [
  ['Ingest', 'Tariffs, tenders, stop reports from dock systems, ELD geofences and carrier EDI 214, and carrier invoices (EDI 210) are published to one Redpanda topic, keyed by tenant and shipment.'],
  ['Claim', 'The auditor opens a Postgres transaction scoped to the tenant by row-level security, and claims the event ID in the inbox. A redelivered event stops here.'],
  ['Rebuild', "The shipment's stop timeline is rebuilt from the full set of evidence: most trusted source wins, first arrival and last departure within a source. Order never matters."],
  ['Audit', 'Each invoice line is rated against the pinned tariff version. Findings, the dispute economics and an evidence hash over every input produce the verdict.'],
  ['Book', 'If the hash changed, the verdict is versioned, the old journal entry reversed and the new one booked, and outbound messages written to the outbox, all in the same commit.'],
  ['Relay', 'A separate relay publishes outbox rows to the verdict and journal topics with SKIP LOCKED batches. Offsets are committed only after the database commit.'],
]

export function HowItWorks() {
  return (
    <div className="grid gap-px bg-line lg:grid-cols-[1.25fr_1fr]">
      <div className="bg-[#070707] p-4">
        <Label>Pipeline</Label>
        <ol className="mt-3 space-y-3">
          {steps.map(([t, body], i) => (
            <li key={t} className="grid grid-cols-[2.25rem_1fr] gap-3">
              <span className="flex h-9 w-9 items-center justify-center border border-line font-mono text-xs font-bold text-red-500">{String(i + 1).padStart(2, '0')}</span>
              <div>
                <p className="font-display text-sm font-semibold uppercase tracking-[0.04em] text-white">{t}</p>
                <p className="text-sm text-dim">{body}</p>
              </div>
            </li>
          ))}
        </ol>
      </div>
      <div className="space-y-4 bg-[#070707] p-4">
        <div>
          <Label>The hard part</Label>
          <p className="mt-2 text-sm text-body">
            A detention charge is only as good as the timestamps behind it, and those arrive from three systems, out of order, duplicated,
            sometimes days after the invoice. Tollgate has to reach the same verdict and the same ledger whatever order the broker delivers
            them in, and correct itself when late evidence lands without double-booking a cent.
          </p>
        </div>
        <div>
          <Label>Guarantees</Label>
          <ul className="mt-2 space-y-1.5 text-sm text-dim">
            {[
              'At-least-once delivery, exactly-once effects (inbox + evidence hash + outbox)',
              'Tenant isolation in the database, not just the API (Postgres RLS, FORCE, non-owner role)',
              'Append-only verdicts and postings; every journal version balances at commit',
              'Integer cents everywhere; tariff versions immutable once published',
            ].map((g) => (
              <li key={g} className="flex gap-2"><span className="mt-2 h-1.5 w-1.5 shrink-0 bg-red-500" />{g}</li>
            ))}
          </ul>
        </div>
        <div>
          <Label>This page</Label>
          <p className="mt-2 text-sm text-dim">
            The engine running here is the Go backend compiled to WebAssembly, with an in-memory store that has the same transaction
            semantics as Postgres. Shippers, carriers and every figure are simulated.
          </p>
          <a className="tm-textlink mt-3 inline-block text-sm text-white" href="https://github.com/TECHMINEs/Tollgate" target="_blank" rel="noreferrer">
            Source and design doc
          </a>
        </div>
      </div>
    </div>
  )
}
