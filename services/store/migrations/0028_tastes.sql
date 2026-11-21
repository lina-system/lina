-- Refined tastes belong to the agent whose journal supplied their evidence (lina#357).
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE DOMAIN memory.taste_valence AS text CHECK (VALUE IN ('like', 'dislike', 'indifferent'));
CREATE DOMAIN memory.taste_strength AS numeric CHECK (VALUE BETWEEN 0 AND 1);
CREATE DOMAIN memory.taste_status AS text CHECK (VALUE IN ('candidate', 'active', 'core', 'dormant', 'forgotten'));
CREATE DOMAIN memory.taste_route AS text CHECK (VALUE IN ('exposure', 'outcome', 'reaction', 'consistency', 'temperament'));

CREATE TABLE memory.tastes (
    agent_id text NOT NULL,
    id bigint GENERATED ALWAYS AS IDENTITY,
    target_kind text NOT NULL CHECK (target_kind IN ('topic', 'task_type', 'tone', 'tool', 'method', 'media', 'other')),
    label text CHECK (label <> '' AND label = btrim(label)),
    label_hash bytea NOT NULL CHECK (octet_length(label_hash) = 32),
    valence memory.taste_valence,
    strength memory.taste_strength,
    status memory.taste_status NOT NULL,
    origin memory.taste_route,
    first_seen date NOT NULL DEFAULT CURRENT_DATE,
    baseline text,
    protected_passed boolean,
    protected_at timestamptz,
    protected_rule text,
    forgotten_on date,
    forgotten_by text CHECK (forgotten_by = 'owner'),
    live boolean GENERATED ALWAYS AS (status <> 'forgotten') STORED,
    PRIMARY KEY (agent_id, id),
    UNIQUE (agent_id, id, live),
    CHECK (origin <> 'temperament' OR strength = 0.3),
    CHECK (first_seen <= CURRENT_DATE),
    CHECK (forgotten_on BETWEEN first_seen AND CURRENT_DATE),
    CHECK (CASE WHEN status = 'forgotten' THEN
               forgotten_on IS NOT NULL AND forgotten_by IS NOT NULL AND label IS NULL AND valence IS NULL
               AND strength IS NULL AND origin IS NULL AND baseline IS NULL AND protected_passed IS NULL
               AND protected_at IS NULL AND protected_rule IS NULL
           ELSE
               forgotten_on IS NULL AND forgotten_by IS NULL AND label IS NOT NULL AND valence IS NOT NULL
               AND strength IS NOT NULL AND origin IS NOT NULL
           END),
    -- A live target reserves its slot indefinitely. Forget shortens that reservation;
    -- the exclusion index arbitrates even inserts racing an uncommitted forget.
    EXCLUDE USING gist (agent_id WITH =, target_kind WITH =, label_hash WITH =,
                        daterange(first_seen, forgotten_on + 90, '[)') WITH &&)
);

CREATE TABLE memory.taste_evidence (
    agent_id text NOT NULL,
    taste_id bigint NOT NULL,
    journal_uid text NOT NULL,
    message_uids text[] NOT NULL DEFAULT '{}',
    observed_on date NOT NULL,
    route memory.taste_route NOT NULL,
    sign smallint NOT NULL CHECK (sign IN (-1, 1)),
    weight memory.taste_strength NOT NULL,
    note text,
    PRIMARY KEY (agent_id, taste_id, journal_uid),
    -- A forgotten parent can never acquire evidence again, even across concurrent transactions.
    live boolean GENERATED ALWAYS AS (true) STORED,
    FOREIGN KEY (agent_id, taste_id, live) REFERENCES memory.tastes (agent_id, id, live)
);

CREATE TABLE memory.taste_events (
    agent_id text NOT NULL,
    taste_id bigint NOT NULL,
    event_id bigint GENERATED ALWAYS AS IDENTITY,
    changed_at timestamptz NOT NULL DEFAULT now(),
    reason text NOT NULL CHECK (reason IN ('created', 'strength_changed', 'status_changed', 'valence_changed',
                                           'forgotten', 'correction')),
    changed_by text NOT NULL CHECK (changed_by IN ('agent', 'review', 'owner')),
    from_status memory.taste_status,
    to_status memory.taste_status,
    from_strength memory.taste_strength,
    to_strength memory.taste_strength,
    from_valence memory.taste_valence,
    to_valence memory.taste_valence,
    PRIMARY KEY (agent_id, taste_id, event_id)
);

SELECT public.lina_apply_agent_rls('memory.tastes');
SELECT public.lina_apply_agent_rls('memory.taste_evidence');
SELECT public.lina_apply_agent_rls('memory.taste_events');

CREATE FUNCTION memory.taste_check_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    cited text;
    expected_source text;
BEGIN
    -- Lock each source for the transaction so a simultaneous forget cannot slip between
    -- checking the row and recording its citation. A journal is specifically a heart row.
    FOR cited, expected_source IN
        SELECT NEW.journal_uid, 'heart' UNION ALL SELECT unnest(NEW.message_uids), 'agent'
    LOOP
        PERFORM 1 FROM memory.messages
            WHERE uid = cited AND source = expected_source AND agent_id = NEW.agent_id AND forgotten_at IS NULL
              -- Same speaker contract as hermes_cli.heart.evidence.lookup.
              AND (speaker <> 'agent' OR speaker_ref = 'agent:' || agent_id)
              AND (speaker <> 'outside' OR role IS DISTINCT FROM 'user')
            FOR SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'taste evidence requires an available source for this agent' USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NEW;
END $$;
CREATE TRIGGER taste_check_evidence BEFORE INSERT OR UPDATE ON memory.taste_evidence
    FOR EACH ROW EXECUTE FUNCTION memory.taste_check_evidence();

CREATE FUNCTION memory.taste_keep_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'taste events are append-only' USING ERRCODE = '23514';
    END IF;
    -- An event needs a parent when written, but outlives a candidate's expiry.
    PERFORM 1 FROM memory.tastes WHERE agent_id = NEW.agent_id AND id = NEW.taste_id FOR KEY SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'taste event requires an existing taste' USING ERRCODE = '23503';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER taste_events_append_only BEFORE INSERT OR UPDATE OR DELETE ON memory.taste_events
    FOR EACH ROW EXECUTE FUNCTION memory.taste_keep_history();

CREATE FUNCTION memory.taste_write() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP <> 'INSERT' AND OLD.status = 'forgotten' THEN
        RAISE EXCEPTION 'taste tombstones are immutable' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.status = 'candidate' AND OLD.first_seen <= CURRENT_DATE - 14 THEN
            -- Recent evidence still pins the parent through its live-row foreign key.
            DELETE FROM memory.taste_evidence WHERE agent_id = OLD.agent_id AND taste_id = OLD.id
                                                AND observed_on <= CURRENT_DATE - 14;
        ELSIF EXISTS (SELECT 1 FROM memory.taste_events WHERE agent_id = OLD.agent_id AND taste_id = OLD.id) THEN
            RAISE EXCEPTION 'taste with history can only be deleted at candidate expiry' USING ERRCODE = '23503';
        END IF;
        RETURN OLD;
    END IF;
    IF TG_OP = 'UPDATE' AND
        (NEW.agent_id, NEW.id, NEW.target_kind, NEW.first_seen, NEW.label) IS DISTINCT FROM
        (OLD.agent_id, OLD.id, OLD.target_kind, OLD.first_seen, OLD.label) THEN
        RAISE EXCEPTION 'taste identity is immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.status <> 'forgotten' THEN
        NEW.label_hash := sha256(convert_to(NEW.label, 'UTF8'));
    ELSIF TG_OP = 'UPDATE' THEN
        NEW.label_hash := OLD.label_hash;
        NEW.forgotten_on := CURRENT_DATE;
        NEW.label := NULL;
        NEW.valence := NULL;
        NEW.strength := NULL;
        NEW.origin := NULL;
        NEW.baseline := NULL;
        NEW.protected_passed := NULL;
        NEW.protected_at := NULL;
        NEW.protected_rule := NULL;
        DELETE FROM memory.taste_evidence WHERE agent_id = OLD.agent_id AND taste_id = OLD.id;
        INSERT INTO memory.taste_events (agent_id, taste_id, reason, changed_by, from_status, to_status)
            VALUES (OLD.agent_id, OLD.id, 'forgotten', NEW.forgotten_by, OLD.status, NEW.status);
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER taste_write BEFORE INSERT OR UPDATE OR DELETE ON memory.tastes
    FOR EACH ROW EXECUTE FUNCTION memory.taste_write();
