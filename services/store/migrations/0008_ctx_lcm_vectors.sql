-- LCM-X's summary vectors in vec.embeddings (lina#16 on lina#19's table): kind 'summary', item_id = the
-- store-wide item_id of LCM-X's (embedded_id, identity_hash) pair, model = the model of LCM-X's embedding profile. lcm_embedding_vectors keeps LCM-X's name and columns as a
-- view; its rows are written and deleted through it. vec.embeddings holds 1024-wide vectors of the one embedding
-- model (docs/store.md): a vector of another width is refused, never stored elsewhere.
-- A summary vector is LCM-X's (embedded_id, identity_hash) row, as in its SQLite table: two profiles of one model
-- keep a vector each, and a vector outlives its summary node until LCM-X purges it. lcm_summary_vector_keys gives
-- each such pair the vec.embeddings item_id its vector is stored under.
CREATE TABLE IF NOT EXISTS ctx.lcm_summary_vector_keys (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    embedded_id text NOT NULL,
    identity_hash text NOT NULL,
    item_id bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    PRIMARY KEY (agent_id, embedded_id, identity_hash)
);
CREATE OR REPLACE VIEW ctx.lcm_embedding_vectors WITH (security_invoker = true) AS
SELECT k.item_id AS rowid,
       k.embedded_id,
       k.identity_hash,
       subvector(e.embedding, 1, p.dim::int)::vector AS vec,
       NULL::text AS access_scope
FROM ctx.lcm_summary_vector_keys AS k
JOIN ctx.lcm_embedding_profile AS p ON p.identity_hash = k.identity_hash
JOIN vec.embeddings AS e ON e.kind = 'summary' AND e.item_id = k.item_id AND e.model = p.model_name;


CREATE OR REPLACE FUNCTION ctx.lcm_embedding_vectors_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    model_name text;
    profile_dtype text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM vec.embeddings AS e
         USING ctx.lcm_embedding_profile AS p
         WHERE e.kind = 'summary' AND p.identity_hash = OLD.identity_hash AND e.model = p.model_name
           AND e.item_id = OLD.rowid;
        DELETE FROM ctx.lcm_summary_vector_keys WHERE item_id = OLD.rowid;
        RETURN OLD;
    END IF;
    SELECT p.model_name, p.dtype INTO model_name, profile_dtype FROM ctx.lcm_embedding_profile AS p WHERE p.identity_hash = NEW.identity_hash;
    IF profile_dtype IS DISTINCT FROM 'float32' AND model_name IS NOT NULL THEN
        PERFORM ctx.lcm_raise('vectors on the memory store are float32 (the one embedding model, lina#19); '
                              'this profile stores ' || profile_dtype);
    END IF;
    IF model_name IS NULL THEN
        PERFORM ctx.lcm_raise('no embedding profile ' || NEW.identity_hash);
    END IF;
    IF vector_dims(NEW.vec) <> 1024 THEN
        PERFORM ctx.lcm_raise('summary vectors on the memory store are 1024 wide (the one embedding model, lina#19); '
                              'this profile writes ' || vector_dims(NEW.vec));
    END IF;
    INSERT INTO ctx.lcm_summary_vector_keys (embedded_id, identity_hash) VALUES (NEW.embedded_id, NEW.identity_hash)
    ON CONFLICT (agent_id, embedded_id, identity_hash) DO NOTHING;
    INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding)
    VALUES ('summary', (SELECT k.item_id FROM ctx.lcm_summary_vector_keys AS k
                         WHERE k.agent_id = public.lina_current_agent() AND k.embedded_id = NEW.embedded_id
                           AND k.identity_hash = NEW.identity_hash),
            public.lina_current_agent(), model_name, 1024,
            NEW.vec)
    ON CONFLICT (kind, item_id, model) DO UPDATE SET embedding = EXCLUDED.embedding;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_insert INSTEAD OF INSERT ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_delete INSTEAD OF DELETE ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();

-- vec.chunks holds the text chunks of memory.messages for every source (lina#19). A chunk that is a part of one
-- message, as LCM-X's are, says which part: chunk_index (NULL for a chunk that is whole messages, as #19's are).
-- A source that keeps its own id for a chunk says so in source_ref (LCM-X: its chunk id `<store_id>:<chunk_index>`), so
-- two messages of the same text (one memory.messages row) keep a chunk each, each with its own vector. Both are NULL
-- for #19's rows, which are keyed as before.
ALTER TABLE vec.chunks ADD COLUMN IF NOT EXISTS chunk_index int;
ALTER TABLE vec.chunks ADD COLUMN IF NOT EXISTS source_ref text;
ALTER TABLE vec.chunks ADD COLUMN IF NOT EXISTS lcm_identity_hash text;
DROP INDEX IF EXISTS vec.chunks_source_messages;
DROP INDEX IF EXISTS vec.chunks_source_messages_part;
CREATE UNIQUE INDEX IF NOT EXISTS chunks_source_messages_part
    ON vec.chunks (source, message_ids, chunk_index, source_ref, lcm_identity_hash) NULLS NOT DISTINCT;
-- LCM-X finds its chunk rows by its chunk id.
CREATE INDEX IF NOT EXISTS chunks_lcm_source_ref ON vec.chunks (source_ref) WHERE source = 'lcm-x';

-- LCM-X's chunk vectors: a chunk is a span of one message (chunk_id `<store_id>:<chunk_index>`). It is one vec.chunks
-- row (source 'lcm-x', message_ids = the memory.messages id of the message's text, chunk_index, source_ref = the
-- chunk id, text = the span) and one vec.embeddings row of kind 'chunk'. LCM-X writes the vector before its
-- lcm_chunk_meta row, so the chunk starts with the message's whole text and lcm_chunk_meta narrows it to the span.
-- Two LCM-X messages with the same text share their memory.messages row but not their chunks.
-- A chunk id LCM-X did not make itself (record_chunk_embedding takes any id) is placed by its lcm_chunk_meta row
-- (store_id, chunk_index); a vector written before that row waits in lcm_chunk_vectors_pending and is published
-- when the row arrives.
CREATE OR REPLACE FUNCTION ctx.lcm_chunk_place(chunk_id text, OUT store_id bigint, OUT chunk_index int)
    LANGUAGE sql STABLE AS $$
    SELECT CASE WHEN chunk_id ~ '^[0-9]+:[0-9]+$' THEN split_part(chunk_id, ':', 1)::bigint
                ELSE (SELECT m.store_id FROM ctx.lcm_chunk_meta AS m
                       WHERE m.chunk_id = $1 AND m.store_id IS NOT NULL LIMIT 1) END,
           CASE WHEN chunk_id ~ '^[0-9]+:[0-9]+$' THEN split_part(chunk_id, ':', 2)::int
                ELSE (SELECT m.chunk_index::int FROM ctx.lcm_chunk_meta AS m
                       WHERE m.chunk_id = $1 AND m.store_id IS NOT NULL LIMIT 1) END $$;
CREATE OR REPLACE FUNCTION ctx.lcm_chunk_message(chunk_id text, OUT message_id bigint, OUT content text)
    LANGUAGE sql STABLE AS $$
    SELECT m.message_id, m.content FROM ctx.lcm_messages AS p JOIN memory.messages AS m ON m.uid = p.text_uid
     WHERE p.store_id = (SELECT place.store_id FROM ctx.lcm_chunk_place(chunk_id) AS place) $$;
CREATE TABLE IF NOT EXISTS ctx.lcm_chunk_vectors_pending (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    chunk_id text NOT NULL,
    identity_hash text NOT NULL,
    vec vector NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, chunk_id, identity_hash)
);

-- The arms pass the stored vector through and the outer SELECT cuts it to the profile's width: a join that keeps a
-- few rows (LCM-X's selected chunk ids) then reads only those vectors, not every vector of the identity.
CREATE OR REPLACE VIEW ctx.lcm_chunk_vectors WITH (security_invoker = true) AS
SELECT u.rowid, u.chunk_id, u.identity_hash, subvector(u.stored, 1, u.dim)::vector AS vec, u.access_scope
FROM (
SELECT c.chunk_id AS rowid,
       c.source_ref AS chunk_id,
       prof.identity_hash,
       e.embedding AS stored,
       prof.dim::int AS dim,
       NULL::text AS access_scope
FROM vec.chunks AS c
JOIN vec.embeddings AS e ON e.kind = 'chunk' AND e.item_id = c.chunk_id
JOIN memory.messages AS m ON m.message_id = c.message_ids[1]
JOIN ctx.lcm_messages AS p ON p.text_uid = m.uid
    AND p.store_id = CASE WHEN c.source_ref ~ '^[0-9]+:[0-9]+$' THEN split_part(c.source_ref, ':', 1)::bigint
                          ELSE (SELECT place.store_id FROM ctx.lcm_chunk_place(c.source_ref) AS place) END
JOIN ctx.lcm_embedding_profile AS prof ON prof.identity_hash = c.lcm_identity_hash
    AND prof.model_name = e.model AND prof.dim <= e.dim
WHERE c.source = 'lcm-x'
UNION ALL
SELECT -w.rowid, w.chunk_id, w.identity_hash, w.vec, prof.dim::int, NULL::text
FROM ctx.lcm_chunk_vectors_pending AS w
JOIN ctx.lcm_embedding_profile AS prof ON prof.identity_hash = w.identity_hash
) AS u;

CREATE OR REPLACE FUNCTION ctx.lcm_chunk_vectors_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    model_name text;
    profile_dtype text;
    msg record;
    vec_chunk bigint;
    width int;
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT * INTO msg FROM ctx.lcm_chunk_message(OLD.chunk_id);
        DELETE FROM vec.embeddings AS e
         USING vec.chunks AS c, ctx.lcm_embedding_profile AS p
         WHERE e.kind = 'chunk' AND e.item_id = c.chunk_id AND c.source = 'lcm-x'
           AND c.source_ref = OLD.chunk_id
           AND c.lcm_identity_hash = OLD.identity_hash
           AND c.message_ids = ARRAY[msg.message_id] AND p.identity_hash = OLD.identity_hash AND e.model = p.model_name;
        DELETE FROM ctx.lcm_chunk_vectors_pending AS w WHERE w.chunk_id = OLD.chunk_id AND w.identity_hash = OLD.identity_hash;
        RETURN OLD;
    END IF;
    SELECT p.model_name, p.dtype INTO model_name, profile_dtype FROM ctx.lcm_embedding_profile AS p WHERE p.identity_hash = NEW.identity_hash;
    IF profile_dtype IS DISTINCT FROM 'float32' AND model_name IS NOT NULL THEN
        PERFORM ctx.lcm_raise('vectors on the memory store are float32 (the one embedding model, lina#19); '
                              'this profile stores ' || profile_dtype);
    END IF;
    width := vector_dims(NEW.vec);
    IF width <> 1024 THEN
        PERFORM ctx.lcm_raise('chunk vectors on the memory store are 1024 wide (the one embedding model, lina#19); '
                              'this profile writes ' || width);
    END IF;
    SELECT * INTO msg FROM ctx.lcm_chunk_message(NEW.chunk_id);
    -- A vector whose message is not (yet) known waits for its lcm_chunk_meta row: an opaque id has no place until then,
    -- and SQLite keeps a chunk vector whatever its id names.
    IF model_name IS NOT NULL AND msg.message_id IS NULL THEN
        INSERT INTO ctx.lcm_chunk_vectors_pending (chunk_id, identity_hash, vec)
        VALUES (NEW.chunk_id, NEW.identity_hash, NEW.vec)
        ON CONFLICT (agent_id, chunk_id, identity_hash) DO UPDATE SET vec = EXCLUDED.vec;
        RETURN NEW;
    END IF;
    IF model_name IS NULL OR msg.message_id IS NULL THEN
        PERFORM ctx.lcm_raise('no embedding profile or message for chunk ' || NEW.chunk_id);
    END IF;
    INSERT INTO vec.chunks (source, agent_id, message_ids, chunk_index, source_ref, lcm_identity_hash, text)
    VALUES ('lcm-x', public.lina_current_agent(), ARRAY[msg.message_id],
            (SELECT place.chunk_index FROM ctx.lcm_chunk_place(NEW.chunk_id) AS place),
            NEW.chunk_id, NEW.identity_hash, coalesce(msg.content, ''))
    ON CONFLICT (source, message_ids, chunk_index, source_ref, lcm_identity_hash) DO UPDATE SET source = EXCLUDED.source
    RETURNING chunk_id INTO vec_chunk;
    INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding)
    VALUES ('chunk', vec_chunk, public.lina_current_agent(), model_name, 1024,
            NEW.vec)
    ON CONFLICT (kind, item_id, model) DO UPDATE SET embedding = EXCLUDED.embedding;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_chunk_vectors_insert INSTEAD OF INSERT ON ctx.lcm_chunk_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_chunk_vectors_write();
CREATE OR REPLACE TRIGGER lcm_chunk_vectors_delete INSTEAD OF DELETE ON ctx.lcm_chunk_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_chunk_vectors_write();

-- The chunk's span, once LCM-X records it.
CREATE OR REPLACE FUNCTION ctx.lcm_chunk_meta_span() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    msg record;
    waiting record;
BEGIN
    FOR waiting IN DELETE FROM ctx.lcm_chunk_vectors_pending AS w WHERE w.chunk_id = NEW.chunk_id
                   RETURNING w.chunk_id, w.identity_hash, w.vec LOOP
        INSERT INTO ctx.lcm_chunk_vectors (chunk_id, identity_hash, vec)
        VALUES (waiting.chunk_id, waiting.identity_hash, waiting.vec);
    END LOOP;
    SELECT * INTO msg FROM ctx.lcm_chunk_message(NEW.chunk_id);
    IF msg.message_id IS NOT NULL AND NEW.char_end > NEW.char_start THEN
        UPDATE vec.chunks
           SET text = substr(coalesce(msg.content, ''), (NEW.char_start + 1)::int, (NEW.char_end - NEW.char_start)::int)
         WHERE source = 'lcm-x' AND source_ref = NEW.chunk_id AND message_ids = ARRAY[msg.message_id];
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_chunk_meta_span AFTER INSERT OR UPDATE OF char_start, char_end ON ctx.lcm_chunk_meta
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_chunk_meta_span();

-- LCM-X stamps access_scope on its vector rows too; on the store a vector's scope is its metadata row's, so an UPDATE
-- of a vector view changes nothing.
CREATE OR REPLACE FUNCTION ctx.lcm_vector_view_update() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_update INSTEAD OF UPDATE ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_vector_view_update();
CREATE OR REPLACE TRIGGER lcm_chunk_vectors_update INSTEAD OF UPDATE ON ctx.lcm_chunk_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_vector_view_update();

-- As for ctx (0007): lina_app refreshes the statistics of the vector tables after a bulk write.
GRANT MAINTAIN ON vec.embeddings, vec.chunks TO lina_app;
-- and on the ctx tables this file adds (0007 granted those that existed then).
GRANT MAINTAIN ON ALL TABLES IN SCHEMA ctx TO lina_app;
