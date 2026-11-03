-- memory.messages: every message once, the one copy every engine reads (lina#12).
-- Applied by `lina store install|upgrade` (hermes_cli/store_migrations.py), recorded in memory.schema_migrations.
-- Column choices (native vs real column) are documented in docs/store.md.

CREATE TABLE IF NOT EXISTS memory.messages (
    message_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- agent:<profile>:<message uid> for agent turns; a later text version of the same message gets #<digest>.
    uid text NOT NULL UNIQUE,
    source text NOT NULL,
    agent_id text,
    session_id text,
    conversation_id text,
    seq_in_session bigint,
    role text,
    content text,
    tool_name text,
    tool_call_id text,
    tool_calls jsonb,
    ts timestamptz,
    observed_at timestamptz NOT NULL DEFAULT now(),
    scope text NOT NULL DEFAULT 'agent' CHECK (scope IN ('agent', 'agents', 'person')),
    native jsonb,
    content_sha256 text NOT NULL,
    forgotten_at timestamptz,
    -- 'simple': no stemming or stop words, so every language tokenizes the same way; CJK goes through trigrams.
    tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED
);

CREATE INDEX IF NOT EXISTS messages_agent_session ON memory.messages (agent_id, session_id, seq_in_session);
CREATE INDEX IF NOT EXISTS messages_tsv ON memory.messages USING gin (tsv);
CREATE INDEX IF NOT EXISTS messages_content_trgm ON memory.messages USING gin (content gin_trgm_ops);
