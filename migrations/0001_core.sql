-- Core schema. Every tenant-owned table carries tenant_id as the first key column, so
-- row-level security (0002) and every index are tenant-scoped from the start.

CREATE TABLE tenants (
    id          text PRIMARY KEY CHECK (id ~ '^[a-z0-9-]{2,63}$'),
    name        text NOT NULL,
    policy      jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- Idempotent consumer: one row per inbound event ever processed.
CREATE TABLE inbox (
    tenant_id    text NOT NULL REFERENCES tenants (id),
    source       text NOT NULL,
    event_id     text NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, source, event_id)
);

-- Tariff versions are immutable; a rate change is a new version.
CREATE TABLE tariffs (
    tenant_id   text NOT NULL REFERENCES tenants (id),
    id          text NOT NULL,
    version     integer NOT NULL CHECK (version >= 1),
    carrier_id  text NOT NULL,
    body        jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id, version)
);

CREATE TABLE shipments (
    tenant_id       text NOT NULL REFERENCES tenants (id),
    id              text NOT NULL,
    carrier_id      text NOT NULL,
    tariff_id       text NOT NULL,
    tariff_version  integer NOT NULL,
    lane_id         text NOT NULL,
    tendered_at     timestamptz NOT NULL,
    body            jsonb NOT NULL,
    PRIMARY KEY (tenant_id, id)
);
CREATE INDEX shipments_by_tariff ON shipments (tenant_id, tariff_id, tariff_version);

-- No foreign key to shipments: stop reports and invoices may arrive before the tender.
CREATE TABLE stop_events (
    tenant_id    text NOT NULL REFERENCES tenants (id),
    source       text NOT NULL CHECK (source IN ('dock_system', 'eld_geofence', 'carrier_edi')),
    event_id     text NOT NULL,
    shipment_id  text NOT NULL,
    stop_seq     integer NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('arrived', 'departed')),
    occurred_at  timestamptz NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, source, event_id)
);
CREATE INDEX stop_events_by_shipment ON stop_events (tenant_id, shipment_id);

CREATE TABLE invoices (
    tenant_id    text NOT NULL REFERENCES tenants (id),
    id           text NOT NULL,
    revision     integer NOT NULL CHECK (revision >= 1),
    shipment_id  text NOT NULL,
    carrier_id   text NOT NULL,
    number       text NOT NULL,
    received_at  timestamptz NOT NULL,
    billed_cents bigint NOT NULL,
    body         jsonb NOT NULL,
    PRIMARY KEY (tenant_id, id, revision)
);
CREATE INDEX invoices_by_shipment ON invoices (tenant_id, shipment_id);
CREATE INDEX invoices_by_number ON invoices (tenant_id, carrier_id, number);

-- Every verdict version is kept: the audit trail of why an invoice was paid as it was.
CREATE TABLE verdicts (
    tenant_id         text NOT NULL REFERENCES tenants (id),
    invoice_id        text NOT NULL,
    version           integer NOT NULL CHECK (version >= 1),
    shipment_id       text NOT NULL,
    invoice_revision  integer NOT NULL,
    carrier_id        text NOT NULL,
    decision          text NOT NULL CHECK (decision IN ('APPROVE', 'APPROVE_VARIANCE', 'SHORT_PAY', 'REJECT', 'HOLD')),
    evidence_hash     text NOT NULL,
    billed_cents      bigint NOT NULL,
    approved_cents    bigint NOT NULL,
    disputed_cents    bigint NOT NULL,
    leakage_cents     bigint NOT NULL,
    body              jsonb NOT NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, invoice_id, version)
);
CREATE INDEX verdicts_by_shipment ON verdicts (tenant_id, shipment_id);

-- Current head of each journal; postings hold the full history.
CREATE TABLE journals (
    tenant_id    text NOT NULL REFERENCES tenants (id),
    journal_key  text NOT NULL,
    version      integer NOT NULL CHECK (version >= 1),
    lines        jsonb NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, journal_key)
);

CREATE TABLE postings (
    tenant_id     text NOT NULL REFERENCES tenants (id),
    journal_key   text NOT NULL,
    version       integer NOT NULL,
    line_no       integer NOT NULL,
    account       text NOT NULL,
    debit_cents   bigint NOT NULL DEFAULT 0 CHECK (debit_cents >= 0),
    credit_cents  bigint NOT NULL DEFAULT 0 CHECK (credit_cents >= 0),
    memo          text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, journal_key, version, line_no),
    CHECK ((debit_cents = 0) <> (credit_cents = 0))
);

-- Transactional outbox: written with the change that caused it, published afterwards.
CREATE TABLE outbox (
    id            bigserial PRIMARY KEY,
    tenant_id     text NOT NULL REFERENCES tenants (id),
    topic         text NOT NULL,
    key           text NOT NULL,
    event_id      text NOT NULL UNIQUE,
    payload       jsonb NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    published_at  timestamptz
);
CREATE INDEX outbox_unpublished ON outbox (id) WHERE published_at IS NULL;

-- History is append-only: corrections are new versions and reversing postings.
CREATE FUNCTION forbid_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE TRIGGER postings_append_only BEFORE UPDATE OR DELETE ON postings
    FOR EACH ROW EXECUTE FUNCTION forbid_change();
CREATE TRIGGER verdicts_append_only BEFORE UPDATE OR DELETE ON verdicts
    FOR EACH ROW EXECUTE FUNCTION forbid_change();

-- Each journal version must balance. Checked at commit, after all its lines are in.
CREATE FUNCTION check_journal_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    net bigint;
BEGIN
    SELECT sum(debit_cents) - sum(credit_cents) INTO net
      FROM postings
     WHERE tenant_id = NEW.tenant_id AND journal_key = NEW.journal_key AND version = NEW.version;
    IF net <> 0 THEN
        RAISE EXCEPTION 'journal % v% is out of balance by % cents', NEW.journal_key, NEW.version, net
            USING ERRCODE = 'check_violation';
    END IF;
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER postings_balanced AFTER INSERT ON postings
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION check_journal_balanced();
