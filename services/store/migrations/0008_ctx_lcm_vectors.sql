-- LCM-X's summary vectors in vec.embeddings (lina#16 on lina#19's table): kind 'summary', item_id = the summary
-- node's store-wide vec_item_id, model = the model of LCM-X's embedding profile. lcm_embedding_vectors keeps LCM-X's name and columns as a
-- view; its rows are written and deleted through it. vec.embeddings holds 1024-wide vectors of the one embedding
-- model (docs/store.md): a narrower profile is zero-padded to 1024, a wider one refused, never stored elsewhere.
CREATE OR REPLACE VIEW ctx.lcm_embedding_vectors WITH (security_invoker = true) AS
SELECT n.node_id AS rowid,
       n.node_id::text AS embedded_id,
       p.identity_hash,
       subvector(e.embedding, 1, p.dim::int)::vector AS vec,
       NULL::text AS access_scope
FROM vec.embeddings AS e
JOIN ctx.summary_nodes AS n ON n.vec_item_id = e.item_id
JOIN ctx.lcm_embedding_profile AS p ON p.model_name = e.model AND p.dim <= e.dim
WHERE e.kind = 'summary';

CREATE OR REPLACE FUNCTION ctx.lcm_embedding_vectors_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    model_name text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM vec.embeddings AS e
         USING ctx.lcm_embedding_profile AS p
         WHERE e.kind = 'summary' AND p.identity_hash = OLD.identity_hash AND e.model = p.model_name
           AND e.item_id = (SELECT n.vec_item_id FROM ctx.summary_nodes AS n WHERE n.node_id = OLD.embedded_id::bigint);
        RETURN OLD;
    END IF;
    SELECT p.model_name INTO model_name FROM ctx.lcm_embedding_profile AS p WHERE p.identity_hash = NEW.identity_hash;
    IF model_name IS NULL THEN
        PERFORM ctx.lcm_raise('no embedding profile ' || NEW.identity_hash);
    END IF;
    IF vector_dims(NEW.vec) > 1024 THEN
        PERFORM ctx.lcm_raise('summary vectors on the memory store are at most 1024 wide (the one embedding model); '
                              'this profile writes ' || vector_dims(NEW.vec));
    END IF;
    -- A narrower vector is stored zero-padded (cosine and dot products are unchanged) and read back at its width.
    INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding)
    VALUES ('summary', (SELECT n.vec_item_id FROM ctx.summary_nodes AS n WHERE n.node_id = NEW.embedded_id::bigint),
            public.lina_current_agent(), model_name, 1024,
            CASE WHEN vector_dims(NEW.vec) = 1024 THEN NEW.vec
                 ELSE (NEW.vec::real[] || array_fill(0::real, ARRAY[1024 - vector_dims(NEW.vec)]))::vector END)
    ON CONFLICT (kind, item_id, model) DO UPDATE SET embedding = EXCLUDED.embedding;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_insert INSTEAD OF INSERT ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_delete INSTEAD OF DELETE ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();

-- LCM-X's chunk vectors: a chunk is a span of one message (chunk_id `<store_id>:<chunk_index>`). It is one vec.chunks
-- row (source 'lcm-x:<chunk_index>', message_ids = the memory.messages id of the message's text, text = the span)
-- and one vec.embeddings row of kind 'chunk'. LCM-X writes the vector before its lcm_chunk_meta row, so the
-- chunk starts with the message's whole text and lcm_chunk_meta narrows it to the span. Two LCM-X messages with
-- the same text share their memory.messages row, and so their chunks and vectors.
CREATE OR REPLACE FUNCTION ctx.lcm_chunk_message(chunk_id text, OUT message_id bigint, OUT content text)
    LANGUAGE sql STABLE AS $$
    SELECT m.message_id, m.content FROM ctx.lcm_messages AS p JOIN memory.messages AS m ON m.uid = p.text_uid
     WHERE p.store_id = split_part(chunk_id, ':', 1)::bigint $$;

CREATE OR REPLACE VIEW ctx.lcm_chunk_vectors WITH (security_invoker = true) AS
SELECT c.chunk_id AS rowid,
       p.store_id || ':' || split_part(c.source, ':', 2) AS chunk_id,
       prof.identity_hash,
       subvector(e.embedding, 1, prof.dim::int)::vector AS vec,
       NULL::text AS access_scope
FROM vec.chunks AS c
JOIN vec.embeddings AS e ON e.kind = 'chunk' AND e.item_id = c.chunk_id
JOIN memory.messages AS m ON m.message_id = c.message_ids[1]
JOIN ctx.lcm_messages AS p ON p.text_uid = m.uid
JOIN ctx.lcm_embedding_profile AS prof ON prof.model_name = e.model AND prof.dim <= e.dim
WHERE c.source LIKE 'lcm-x:%';

CREATE OR REPLACE FUNCTION ctx.lcm_chunk_vectors_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    model_name text;
    msg record;
    vec_chunk bigint;
    width int;
BEGIN
    IF TG_OP = 'DELETE' THEN
        SELECT * INTO msg FROM ctx.lcm_chunk_message(OLD.chunk_id);
        DELETE FROM vec.embeddings AS e
         USING vec.chunks AS c, ctx.lcm_embedding_profile AS p
         WHERE e.kind = 'chunk' AND e.item_id = c.chunk_id AND c.source = 'lcm-x:' || split_part(OLD.chunk_id, ':', 2)
           AND c.message_ids = ARRAY[msg.message_id] AND p.identity_hash = OLD.identity_hash AND e.model = p.model_name;
        RETURN OLD;
    END IF;
    SELECT p.model_name INTO model_name FROM ctx.lcm_embedding_profile AS p WHERE p.identity_hash = NEW.identity_hash;
    SELECT * INTO msg FROM ctx.lcm_chunk_message(NEW.chunk_id);
    IF model_name IS NULL OR msg.message_id IS NULL THEN
        PERFORM ctx.lcm_raise('no embedding profile or message for chunk ' || NEW.chunk_id);
    END IF;
    width := vector_dims(NEW.vec);
    IF width > 1024 THEN
        PERFORM ctx.lcm_raise('chunk vectors on the memory store are at most 1024 wide (the one embedding model); '
                              'this profile writes ' || width);
    END IF;
    INSERT INTO vec.chunks (source, agent_id, message_ids, text)
    VALUES ('lcm-x:' || split_part(NEW.chunk_id, ':', 2), public.lina_current_agent(), ARRAY[msg.message_id],
            coalesce(msg.content, ''))
    ON CONFLICT (source, message_ids) DO UPDATE SET source = EXCLUDED.source
    RETURNING chunk_id INTO vec_chunk;
    INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding)
    VALUES ('chunk', vec_chunk, public.lina_current_agent(), model_name, 1024,
            CASE WHEN width = 1024 THEN NEW.vec
                 ELSE (NEW.vec::real[] || array_fill(0::real, ARRAY[1024 - width]))::vector END)
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
BEGIN
    SELECT * INTO msg FROM ctx.lcm_chunk_message(NEW.chunk_id);
    IF msg.message_id IS NOT NULL AND NEW.char_end > NEW.char_start THEN
        UPDATE vec.chunks
           SET text = substr(coalesce(msg.content, ''), (NEW.char_start + 1)::int, (NEW.char_end - NEW.char_start)::int)
         WHERE source = 'lcm-x:' || NEW.chunk_index AND message_ids = ARRAY[msg.message_id];
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
