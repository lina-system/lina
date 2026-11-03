-- Owner-level external messages are readable, never writable, by every agent (lina#18).
ALTER TABLE memory.messages ADD COLUMN IF NOT EXISTS author text;

DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'memory.messages'::regclass AND polname = 'lina_shared_messages') THEN
        CREATE POLICY lina_shared_messages ON memory.messages
            FOR SELECT TO lina_app USING (scope = 'agents');
    END IF;
END $$;

-- Mutable ingestion projections, not message text. Default privileges must not expose owner state.
CREATE TABLE IF NOT EXISTS memory.external_message_cursors (
    spool text PRIMARY KEY,
    byte_offset bigint NOT NULL CHECK (byte_offset >= 0),
    prefix_sha256 text NOT NULL
);
CREATE TABLE IF NOT EXISTS memory.external_message_heads (
    uid text PRIMARY KEY,
    current_uid text NOT NULL,
    ingest_path text NOT NULL
);
REVOKE ALL ON memory.external_message_cursors, memory.external_message_heads FROM lina_app;
