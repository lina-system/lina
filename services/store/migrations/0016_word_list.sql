-- The owner's word list (lina#378): words to avoid and the word to use instead, shared by every agent. Rows have no
-- agent_id (no agent owns them), so the agent policy lina_agent_rows does not apply; as 0005 and 0009 do for the
-- owner-level external messages, a SELECT-only policy lets every agent read the list and nothing lets an agent write
-- it. The owner path (`lina words`, role lina) and the refiner (role lina_refiner) write it. Each correction's flagged
-- and fixed sentences stay in memory.word_pairs, which no agent can read (docs/korean.md).

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lina_refiner') THEN
        CREATE ROLE lina_refiner NOLOGIN;
    END IF;
END $$;
GRANT USAGE ON SCHEMA memory TO lina_refiner;

CREATE TABLE IF NOT EXISTS memory.word_list (
    entry_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    avoid_word text NOT NULL CHECK (btrim(avoid_word) <> ''),
    use_word text NOT NULL CHECK (btrim(use_word) <> ''),
    -- hermes_cli/korean/words.py REASONS; docs/korean.md says what each means.
    reason text NOT NULL CHECK (reason IN ('calque', 'coined', 'translationese', 'owner_term', 'other')),
    source_uid text,
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    retired_source_uid text,
    CHECK (avoid_word <> use_word)
);
-- The same correction twice is one entry: one active row per (avoid, use) pair.
CREATE UNIQUE INDEX IF NOT EXISTS word_list_active ON memory.word_list (avoid_word, use_word) WHERE retired_at IS NULL;

CREATE TABLE IF NOT EXISTS memory.word_pairs (
    pair_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id bigint NOT NULL REFERENCES memory.word_list (entry_id) ON DELETE CASCADE,
    flagged text NOT NULL CHECK (btrim(flagged) <> ''),
    fixed text NOT NULL CHECK (btrim(fixed) <> ''),
    source_uid text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (entry_id, flagged, fixed)
);

ALTER TABLE memory.word_list ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.word_list FORCE ROW LEVEL SECURITY;
ALTER TABLE memory.word_pairs ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.word_pairs FORCE ROW LEVEL SECURITY;

-- The schema's default privileges give lina_app every new table; take the writes back, and the pairs entirely.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON memory.word_list FROM lina_app;
REVOKE ALL ON memory.word_pairs FROM lina_app;
GRANT SELECT ON memory.word_list TO lina_app;
GRANT SELECT, INSERT, UPDATE ON memory.word_list TO lina_refiner;
GRANT SELECT, INSERT ON memory.word_pairs TO lina_refiner;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'memory.word_list'::regclass AND polname = 'lina_shared_words') THEN
        CREATE POLICY lina_shared_words ON memory.word_list FOR SELECT TO lina_app USING (true);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'memory.word_list'::regclass AND polname = 'lina_refiner_words') THEN
        CREATE POLICY lina_refiner_words ON memory.word_list TO lina_refiner USING (true) WITH CHECK (true);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'memory.word_pairs'::regclass AND polname = 'lina_refiner_pairs') THEN
        CREATE POLICY lina_refiner_pairs ON memory.word_pairs TO lina_refiner USING (true) WITH CHECK (true);
    END IF;
END $$;
