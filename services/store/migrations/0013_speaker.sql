-- Who said it (lina#324, absorbing #310 and #313): every row of memory.messages carries its speaker kind, gateway
-- notes are their own parts beside the owner's message, derived items show the kinds of their sources, and a
-- handle joins a person only through a link the owner confirmed. Rules and formats: docs/store.md.

-- owner | agent (speaker_ref agent:<profile>) | gateway | outside (speaker_ref <platform>:<author id>, tool:<name>)
-- | unknown. Never guessed from text.
ALTER TABLE memory.messages ADD COLUMN IF NOT EXISTS speaker text;
ALTER TABLE memory.messages ADD COLUMN IF NOT EXISTS speaker_ref text;

-- The kind a row's stored fields alone decide. A user turn of an agent is not decided by them (its route is not
-- stored): it is unknown until lina-memory fills it from the policy table when the turn finishes.
CREATE OR REPLACE FUNCTION memory.speaker_by_rule(source text, role text, tool_name text, author text,
                                                  agent_id text, native jsonb, OUT kind text, OUT ref text)
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE
               WHEN source = 'messages' THEN 'outside'
               WHEN source = 'heart' THEN 'agent'
               WHEN role = 'assistant' THEN 'agent'
               WHEN role = 'tool' THEN 'outside'
               WHEN role = 'user' AND (native->>'_compressed_summary') = 'true' THEN 'agent'
               ELSE 'unknown' END,
           CASE
               WHEN source = 'messages' THEN author
               WHEN source = 'heart' OR role = 'assistant'
                    OR (role = 'user' AND (native->>'_compressed_summary') = 'true') THEN 'agent:' || agent_id
               WHEN role = 'tool' THEN 'tool:' || coalesce(tool_name, '')
           END $$;

UPDATE memory.messages SET (speaker, speaker_ref) =
    (SELECT r.kind, r.ref FROM memory.speaker_by_rule(source, role, tool_name, author, agent_id, native) AS r)
WHERE speaker IS NULL;

-- A writer that names no speaker (Honcho, external messages, the heart journal, an agent without lina-memory) gets
-- the rule's kind, so no row is ever without one.
CREATE OR REPLACE FUNCTION memory.messages_speaker_by_rule() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT r.kind, coalesce(NEW.speaker_ref, r.ref) INTO NEW.speaker, NEW.speaker_ref
    FROM memory.speaker_by_rule(NEW.source, NEW.role, NEW.tool_name, NEW.author, NEW.agent_id, NEW.native) AS r;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER messages_speaker_by_rule BEFORE INSERT ON memory.messages
    FOR EACH ROW WHEN (NEW.speaker IS NULL) EXECUTE FUNCTION memory.messages_speaker_by_rule();

ALTER TABLE memory.messages ALTER COLUMN speaker SET NOT NULL;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'memory.messages'::regclass AND conname = 'messages_speaker_kind') THEN
        ALTER TABLE memory.messages ADD CONSTRAINT messages_speaker_kind
            CHECK (speaker IN ('owner', 'agent', 'gateway', 'outside', 'unknown'));
    END IF;
END $$;

-- A note the gateway added to an inbound message (#310): stored beside the owner's message, never inside its text.
-- The model still reads the note where the gateway put it (the row's api_content); this is the note as memory.
CREATE TABLE IF NOT EXISTS memory.message_parts (
    part_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    -- The owner's message the note belongs to; the note goes with it.
    message_id bigint NOT NULL REFERENCES memory.messages (message_id) ON DELETE CASCADE,
    ordinal integer NOT NULL,
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    kind text NOT NULL CHECK (kind IN ('reply_quote', 'image_reading', 'voice_note', 'audio_note', 'video_note',
                                       'document_note', 'trigger_note', 'sender_label', 'channel_context')),
    speaker text NOT NULL DEFAULT 'gateway' CHECK (speaker IN ('owner', 'agent', 'gateway', 'outside', 'unknown')),
    -- Whose words the note quotes or reads out (a reply quote of the agent's message: agent; an image reading: the
    -- agent's model), NULL when it quotes nobody.
    quoted_speaker text CHECK (quoted_speaker IN ('owner', 'agent', 'gateway', 'outside', 'unknown')),
    quoted_ref text,
    content text NOT NULL,
    tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', content)) STORED,
    UNIQUE (message_id, ordinal)
);
CREATE INDEX IF NOT EXISTS message_parts_tsv ON memory.message_parts USING gin (tsv);
CREATE INDEX IF NOT EXISTS message_parts_content_trgm ON memory.message_parts USING gin (content gin_trgm_ops);

-- Derived items keep the kinds of their sources: the distinct kinds, sorted, of every message they come from.
-- A heart journal row's sources are the uids it cites; one that is gone counts as unknown.
CREATE OR REPLACE VIEW memory.message_source_speakers WITH (security_invoker = true) AS
SELECT d.uid, array_agg(DISTINCT coalesce(s.speaker, 'unknown') ORDER BY coalesce(s.speaker, 'unknown'))
           AS source_speakers
FROM memory.messages AS d
CROSS JOIN LATERAL jsonb_array_elements_text(CASE WHEN jsonb_typeof(d.native->'evidence') = 'array'
                                                  THEN d.native->'evidence' ELSE '[]'::jsonb END) AS e(uid)
LEFT JOIN memory.messages AS s ON s.uid = e.uid
WHERE d.source = 'heart'
GROUP BY d.uid;

-- An LCM-X summary's sources: the messages under it, through every condensed level.
CREATE OR REPLACE VIEW ctx.summary_speakers WITH (security_invoker = true) AS
WITH RECURSIVE reach (agent_id, node_id, source_type, source_id) AS (
    SELECT n.agent_id, n.node_id, n.source_type, s.value::bigint
    FROM ctx.summary_nodes AS n, jsonb_array_elements_text(n.source_ids::jsonb) AS s
    UNION
    SELECT r.agent_id, r.node_id, c.source_type, s.value::bigint
    FROM reach AS r
    JOIN ctx.summary_nodes AS c ON r.source_type = 'nodes' AND c.agent_id = r.agent_id AND c.node_id = r.source_id
    CROSS JOIN LATERAL jsonb_array_elements_text(c.source_ids::jsonb) AS s
)
SELECT r.agent_id, r.node_id,
       array_agg(DISTINCT coalesce(m.speaker, 'unknown') ORDER BY coalesce(m.speaker, 'unknown')) AS source_speakers
FROM reach AS r
LEFT JOIN ctx.lcm_messages AS p ON p.agent_id = r.agent_id AND p.store_id = r.source_id
LEFT JOIN memory.messages AS m ON m.uid = p.text_uid
WHERE r.source_type = 'messages'
GROUP BY r.agent_id, r.node_id;

-- A handle (an identity key of kind person: gateway_user, messenger_author, honcho_peer, local_user) belongs to a
-- person only through a link (#313). Only a confirmed link joins; a candidate keeps its evidence and joins nothing;
-- an undone link is kept with the sentence that undid it. A handle is confirmed to one person at most.
CREATE TABLE IF NOT EXISTS memory.identity_links (
    source text NOT NULL CHECK (source IN ('gateway_user', 'local_user', 'honcho_peer', 'messenger_author')),
    key text NOT NULL,
    person_id text NOT NULL REFERENCES memory.identities (identity_id),
    status text NOT NULL CHECK (status IN ('confirmed', 'candidate', 'undone')),
    -- Why: the owner's sentence (confirmed, undone), what suggested it (candidate), or the import it came from.
    evidence text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source, key, person_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS identity_links_one_person ON memory.identity_links (source, key)
    WHERE status = 'confirmed';

-- Every external message's author is a person of the registry under its own handle (messenger_author:<author>),
-- with the id lina-memory derives for it (plugins/memory/lina/identity.py, derived_id); a later author is registered
-- the first time it is named.
INSERT INTO memory.identities (identity_id, kind)
SELECT DISTINCT 'person:' || left(encode(sha256(convert_to('person' || chr(31) || 'messenger_author' || chr(31)
                                                            || author, 'UTF8')), 'hex'), 20), 'person'
FROM memory.messages WHERE source = 'messages' AND author IS NOT NULL
ON CONFLICT (identity_id) DO NOTHING;
INSERT INTO memory.identity_keys (kind, source, key, identity_id)
SELECT DISTINCT 'person', 'messenger_author', author,
       'person:' || left(encode(sha256(convert_to('person' || chr(31) || 'messenger_author' || chr(31) || author,
                                                  'UTF8')), 'hex'), 20)
FROM memory.messages WHERE source = 'messages' AND author IS NOT NULL
ON CONFLICT (kind, source, key) DO NOTHING;
CREATE INDEX IF NOT EXISTS identity_links_person ON memory.identity_links (person_id) WHERE status = 'confirmed';
