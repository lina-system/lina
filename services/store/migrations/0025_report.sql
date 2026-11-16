-- Reports (lina#263): when an agent has more to say than a few short chat messages, it writes a report and sends
-- one line with a reference to it; the full report lives on a page that is always current. One row per report in
-- reports.report, one row per version in reports.report_version: an edit adds a version and the old one stays
-- readable. The newest version is the report's body. Written by the `report` tool (tools/report_tool.py) through
-- hermes_state_reports.py; served by hermes_cli/web_routers/lina_reports.py. docs/store.md "Reports".
--
-- Agent-scoped: agent_id is the agent that wrote the report, so lina_agent_rows keeps every agent to its own reports.
-- The owner's client API reads them as the store owner. A private report (written from a session that handles the
-- owner's personal documents, lina#308, or marked private by its author) is served only to the owner's session.

CREATE SCHEMA IF NOT EXISTS reports;

-- Row-level security on agent_id covers reports too: the event trigger (services/store/initdb, widened to brain in
-- 0014) gives every table created below lina_agent_rows.
CREATE OR REPLACE FUNCTION public.lina_agent_rls_on_ddl() RETURNS event_trigger
    LANGUAGE plpgsql AS $$
DECLARE
    obj record;
BEGIN
    FOR obj IN
        SELECT objid FROM pg_event_trigger_ddl_commands()
        WHERE object_type = 'table' AND schema_name IN ('memory', 'ctx', 'ltm', 'vec', 'brain', 'reports')
    LOOP
        IF EXISTS (SELECT 1 FROM pg_attribute
                   WHERE attrelid = obj.objid AND attname = 'agent_id' AND NOT attisdropped) THEN
            PERFORM public.lina_apply_agent_rls(obj.objid::regclass);
        END IF;
    END LOOP;
END $$;

-- card: the work card id as the client API names it (`/work/cards/{id}`); goal: the goal's id (a goal card or a
-- work-record goal). Both optional. version: the newest version's number, the one the body is read from.
CREATE TABLE IF NOT EXISTS reports.report (
    report_id text PRIMARY KEY CHECK (report_id ~ '^rep_[0-9a-f]{12}$'),
    agent_id text NOT NULL,
    title text NOT NULL CHECK (btrim(title) <> ''),
    summary text NOT NULL CHECK (btrim(summary) <> '' AND summary !~ '[\r\n]'),
    card text,
    goal text,
    private boolean NOT NULL DEFAULT false,
    version integer NOT NULL DEFAULT 1 CHECK (version >= 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS report_card ON reports.report (card) WHERE card IS NOT NULL;
CREATE INDEX IF NOT EXISTS report_goal ON reports.report (goal) WHERE goal IS NOT NULL;

-- Every version as written: title, summary and body (markdown) of that edit, and the session it came from.
CREATE TABLE IF NOT EXISTS reports.report_version (
    report_id text NOT NULL REFERENCES reports.report (report_id) ON DELETE CASCADE,
    version integer NOT NULL CHECK (version >= 1),
    agent_id text NOT NULL,
    title text NOT NULL,
    summary text NOT NULL,
    body text NOT NULL,
    session_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (report_id, version)
);

-- Agents add and edit their reports; nothing deletes one through lina_app, so every version stays readable.
GRANT USAGE ON SCHEMA reports TO lina_app;
GRANT SELECT, INSERT, UPDATE ON reports.report TO lina_app;
GRANT SELECT, INSERT ON reports.report_version TO lina_app;
