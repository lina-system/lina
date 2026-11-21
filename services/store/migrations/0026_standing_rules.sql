-- One dated version per owner instruction. The store role owns the table and the writer.
CREATE TABLE IF NOT EXISTS memory.standing_rules (
    rule_id uuid NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    kind text NOT NULL CHECK (kind IN ('conduct', 'filter')),
    scope text NOT NULL CHECK (scope IN ('all', 'agent', 'place')),
    agent_id text NOT NULL CHECK (agent_id = '*' OR agent_id ~ '^agent:[^:[:space:]]+$'),
    place_ref text,
    statement text,
    source_uid text,
    cites text[] NOT NULL DEFAULT '{}',
    said_at timestamptz NOT NULL,
    recorded_by text NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'replaced', 'ended', 'forgotten')),
    replaced_by integer,
    replaced_at timestamptz,
    filter_match jsonb,
    filtered_count bigint CHECK (filtered_count >= 0),
    grade text NOT NULL DEFAULT 'owner' CHECK (grade = 'owner'),
    forgotten_at timestamptz,
    forgotten_by text GENERATED ALWAYS AS (CASE WHEN forgotten_at IS NOT NULL THEN 'owner' END) STORED,
    PRIMARY KEY (rule_id, version),
    CHECK ((scope = 'place') = (place_ref IS NOT NULL)),
    CHECK (place_ref IS NULL OR place_ref ~ '^(channel|source|goal):.+$'),
    CHECK ((scope <> 'all' OR agent_id = '*') AND (scope <> 'agent' OR agent_id <> '*')),
    CHECK ((status = 'forgotten') = (forgotten_at IS NOT NULL)),
    CHECK ((status = 'forgotten') = (source_uid IS NULL)),
    CHECK (CASE WHEN status = 'forgotten' THEN statement IS NULL AND cites = '{}'
                ELSE statement IS NOT NULL AND btrim(statement) <> '' END),
    CHECK (CASE WHEN status = 'replaced' THEN replaced_by = version + 1
                    AND replaced_by IS NOT NULL AND replaced_at IS NOT NULL
                ELSE replaced_by IS NULL AND replaced_at IS NULL END),
    CHECK (CASE WHEN kind = 'filter' AND status <> 'forgotten'
                THEN jsonb_typeof(filter_match) = 'object' AND filter_match IS NOT NULL AND filtered_count IS NOT NULL
                ELSE filter_match IS NULL AND filtered_count IS NULL END)
);
CREATE UNIQUE INDEX IF NOT EXISTS standing_rules_active ON memory.standing_rules (rule_id) WHERE status = 'active';

-- The creation trigger installs lina_agent_rows comparing bare profile names. This
-- table stores identity refs (agent:<profile>) and the shared owner instead.
ALTER POLICY lina_agent_rows ON memory.standing_rules TO lina_app
    USING (agent_id = 'agent:' || public.lina_current_agent() OR agent_id = '*')
    WITH CHECK (false);
REVOKE ALL ON memory.standing_rules FROM lina_app;
GRANT SELECT ON memory.standing_rules TO lina_app;

CREATE OR REPLACE FUNCTION memory.keep_standing_rule_version() RETURNS trigger
    LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF (NEW.rule_id, NEW.version, NEW.kind, NEW.scope, NEW.agent_id, NEW.place_ref, NEW.statement,
        NEW.source_uid, NEW.cites, NEW.said_at, NEW.recorded_by, NEW.filter_match, NEW.grade)
       IS DISTINCT FROM
       (OLD.rule_id, OLD.version, OLD.kind, OLD.scope, OLD.agent_id, OLD.place_ref, OLD.statement,
        OLD.source_uid, OLD.cites, OLD.said_at, OLD.recorded_by, OLD.filter_match, OLD.grade)
       OR (OLD.forgotten_at IS NOT NULL AND OLD.forgotten_at IS DISTINCT FROM NEW.forgotten_at) THEN
        RAISE EXCEPTION 'a recorded standing rule version is immutable' USING ERRCODE = 'check_violation';
    END IF;
    -- The privileged forget path marks forgotten_at, as on memory.messages. Erase all
    -- versions together and keep only the marked row as the tombstone for this ID.
    IF OLD.forgotten_at IS NULL AND NEW.forgotten_at IS NOT NULL THEN
        PERFORM pg_advisory_xact_lock(hashtextextended(OLD.rule_id::text, 0));
        IF EXISTS (SELECT FROM memory.standing_rules WHERE rule_id = OLD.rule_id AND version > OLD.version) THEN
            RAISE EXCEPTION 'mark the latest standing rule version to forget it' USING ERRCODE = 'check_violation';
        END IF;
        DELETE FROM memory.standing_rules WHERE rule_id = OLD.rule_id AND version <> OLD.version;
        NEW.status := 'forgotten';
        NEW.statement := NULL;
        NEW.source_uid := NULL;
        NEW.cites := '{}';
        NEW.place_ref := NULL;
        NEW.scope := CASE WHEN OLD.agent_id = '*' THEN 'all' ELSE 'agent' END;
        NEW.filter_match := NULL;
        NEW.filtered_count := NULL;
        NEW.replaced_by := NULL;
        NEW.replaced_at := NULL;
    END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER keep_standing_rule_version BEFORE UPDATE ON memory.standing_rules
    FOR EACH ROW EXECUTE FUNCTION memory.keep_standing_rule_version();

CREATE OR REPLACE FUNCTION memory.record_standing_rule(
    p_rule_id uuid, p_source_uid text, p_kind text, p_scope text, p_agent_id text, p_place_ref text,
    p_statement text, p_status text DEFAULT 'active', p_cites text[] DEFAULT '{}', p_filter_match jsonb DEFAULT NULL
) RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    caller text := public.lina_current_agent();
    recorder text := 'agent:' || caller;
    source_date timestamptz;
    prior memory.standing_rules%ROWTYPE;
    next_version integer;
BEGIN
    SELECT coalesce(m.ts, m.observed_at) INTO source_date FROM memory.messages m
        WHERE m.uid = p_source_uid AND m.source = 'agent' AND m.role = 'user'
          AND m.agent_id = caller AND nullif(m.session_id, '') IS NOT NULL
          AND m.speaker = 'owner' AND m.grade = 'owner' AND m.forgotten_at IS NULL FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'standing rule needs an owner turn from a session of the recording agent'
            USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT FROM unnest(p_cites) AS c(uid) WHERE NOT EXISTS (
        SELECT FROM memory.messages m WHERE m.uid = c.uid AND m.agent_id = caller
            AND m.forgotten_at IS NULL AND m.grade <> 'external')) THEN
        RAISE EXCEPTION 'standing rule citation is unavailable or external' USING ERRCODE = 'check_violation';
    END IF;

    -- A row lock alone cannot serialize two first versions, when there is no row yet.
    PERFORM pg_advisory_xact_lock(hashtextextended(p_rule_id::text, 0));
    SELECT * INTO prior FROM memory.standing_rules WHERE rule_id = p_rule_id ORDER BY version DESC LIMIT 1;
    IF FOUND AND prior.agent_id NOT IN ('*', recorder) THEN
        RAISE EXCEPTION 'standing rule unavailable' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_status NOT IN ('active', 'ended') OR prior.status = 'forgotten'
       OR (p_status = 'ended' AND prior.status IS DISTINCT FROM 'active') THEN
        RAISE EXCEPTION 'invalid standing rule transition' USING ERRCODE = 'check_violation';
    END IF;
    next_version := coalesce(prior.version, 0) + 1;
    UPDATE memory.standing_rules SET status = 'replaced', replaced_by = next_version, replaced_at = source_date
        WHERE rule_id = p_rule_id AND status = 'active';
    INSERT INTO memory.standing_rules (rule_id, version, kind, scope, agent_id, place_ref, statement,
                                        source_uid, cites, said_at, recorded_by, status, filter_match, filtered_count)
    VALUES (p_rule_id, next_version, p_kind, p_scope, p_agent_id, p_place_ref, p_statement, p_source_uid,
            p_cites, source_date, recorder, p_status, p_filter_match, CASE WHEN p_kind = 'filter' THEN 0 END);
    RETURN next_version;
END $$;
REVOKE ALL ON FUNCTION memory.record_standing_rule(uuid,text,text,text,text,text,text,text,text[],jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.record_standing_rule(uuid,text,text,text,text,text,text,text,text[],jsonb) TO lina_app;
