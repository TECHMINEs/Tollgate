-- Tenant isolation with row-level security.
--
-- The application connects as tollgate_app, which owns nothing and cannot bypass RLS.
-- Each transaction sets app.tenant_id with set_config(..., true), which lasts only for
-- that transaction, so a pooled connection never carries one tenant's scope into
-- another's work. With the setting missing, app_tenant() is NULL and every policy
-- matches nothing: the failure mode is "no rows", never "all rows".

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'tollgate_app') THEN
        CREATE ROLE tollgate_app NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'tollgate_publisher') THEN
        CREATE ROLE tollgate_publisher NOLOGIN;
    END IF;
END $$;

CREATE FUNCTION app_tenant() RETURNS text LANGUAGE sql STABLE AS $$
    SELECT nullif(current_setting('app.tenant_id', true), '')
$$;

GRANT USAGE ON SCHEMA public TO tollgate_app, tollgate_publisher;
GRANT SELECT ON tenants TO tollgate_app;
GRANT SELECT, INSERT ON inbox, tariffs, shipments, stop_events, invoices, verdicts, postings, outbox TO tollgate_app;
GRANT SELECT, INSERT, UPDATE ON journals TO tollgate_app;
GRANT USAGE ON SEQUENCE outbox_id_seq TO tollgate_app;
GRANT SELECT, UPDATE (published_at) ON outbox TO tollgate_publisher;

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_self ON tenants TO tollgate_app USING (id = app_tenant());

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['inbox', 'tariffs', 'shipments', 'stop_events', 'invoices',
                             'verdicts', 'journals', 'postings', 'outbox']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        -- FORCE applies the policies to the table owner too, so a migration-role
        -- connection used by mistake sees nothing rather than everything.
        EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I TO tollgate_app
                 USING (tenant_id = app_tenant()) WITH CHECK (tenant_id = app_tenant())', t);
    END LOOP;
END $$;

-- The outbox publisher relays every tenant's messages, and may only mark them sent.
CREATE POLICY publisher_relay ON outbox TO tollgate_publisher USING (true) WITH CHECK (true);
