#!/bin/sh
# First start of an empty cluster only (docker-entrypoint-initdb.d): the memory schemas, the two
# login roles and row-level security on agent_id. A restored cluster already holds all of this.
set -eu

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
    -v app_password="$LINA_APP_PASSWORD" -v honcho_password="$HONCHO_DB_PASSWORD" <<'SQL'
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS btree_gin;

-- lina_app: what agents connect as; never a table owner, so row-level security always applies.
-- honcho: the Honcho server; owns schema ltm and runs its own migrations there.
CREATE ROLE lina_app LOGIN PASSWORD :'app_password';
CREATE ROLE honcho LOGIN PASSWORD :'honcho_password';

CREATE SCHEMA memory;
CREATE SCHEMA ctx;
CREATE SCHEMA vec;
CREATE SCHEMA ltm AUTHORIZATION honcho;
-- Honcho's migrations run CREATE SCHEMA IF NOT EXISTS, which needs CREATE on the database even when ltm exists.
GRANT CREATE ON DATABASE lina TO honcho;

GRANT USAGE ON SCHEMA memory, ctx, ltm, vec TO lina_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA memory, ctx, vec
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO lina_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA memory, ctx, vec
    GRANT USAGE, SELECT ON SEQUENCES TO lina_app;

-- The agent a session acts for: SET lina.agent_id = '<agent>'. Unset means no rows.
CREATE FUNCTION public.lina_current_agent() RETURNS text
    LANGUAGE sql STABLE AS $$ SELECT nullif(current_setting('lina.agent_id', true), '') $$;

-- Each guard below keeps the nested ALTER TABLE this function runs from re-entering it forever.
CREATE FUNCTION public.lina_apply_agent_rls(rel regclass) RETURNS void
    LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (SELECT relrowsecurity FROM pg_class WHERE oid = rel) THEN
        EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', rel);
    END IF;
    IF NOT (SELECT relforcerowsecurity FROM pg_class WHERE oid = rel) THEN
        EXECUTE format('ALTER TABLE %s FORCE ROW LEVEL SECURITY', rel);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policy WHERE polrelid = rel AND polname = 'lina_agent_rows') THEN
        EXECUTE format('CREATE POLICY lina_agent_rows ON %s '
                       'USING (agent_id::text = public.lina_current_agent()) '
                       'WITH CHECK (agent_id::text = public.lina_current_agent())', rel);
    END IF;
END $$;

-- Every table in the four memory schemas that has (or gains) an agent_id column gets the policy.
CREATE FUNCTION public.lina_agent_rls_on_ddl() RETURNS event_trigger
    LANGUAGE plpgsql AS $$
DECLARE
    obj record;
BEGIN
    FOR obj IN
        SELECT objid FROM pg_event_trigger_ddl_commands()
        WHERE object_type = 'table' AND schema_name IN ('memory', 'ctx', 'ltm', 'vec')
    LOOP
        IF EXISTS (SELECT 1 FROM pg_attribute
                   WHERE attrelid = obj.objid AND attname = 'agent_id' AND NOT attisdropped) THEN
            PERFORM public.lina_apply_agent_rls(obj.objid::regclass);
        END IF;
    END LOOP;
END $$;

CREATE EVENT TRIGGER lina_agent_rls ON ddl_command_end
    WHEN TAG IN ('CREATE TABLE', 'CREATE TABLE AS', 'SELECT INTO', 'ALTER TABLE')
    EXECUTE FUNCTION public.lina_agent_rls_on_ddl();
SQL
