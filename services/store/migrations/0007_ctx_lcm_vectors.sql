-- LCM-X's summary vectors in vec.embeddings (lina#16 on lina#19's table): kind 'summary', item_id = the summary
-- node id, model = the model of LCM-X's embedding profile. lcm_embedding_vectors keeps LCM-X's name and columns as a
-- view; its rows are written and deleted through it. vec.embeddings holds 1024-wide vectors of the one embedding
-- model (docs/store.md), so a profile of another width is refused here instead of being stored elsewhere.
CREATE OR REPLACE VIEW ctx.lcm_embedding_vectors WITH (security_invoker = true) AS
SELECT e.item_id AS rowid,
       e.item_id::text AS embedded_id,
       p.identity_hash,
       e.embedding::vector AS vec
FROM vec.embeddings AS e
JOIN ctx.lcm_embedding_profile AS p ON p.model_name = e.model AND p.dim = e.dim
WHERE e.kind = 'summary';

CREATE OR REPLACE FUNCTION ctx.lcm_embedding_vectors_write() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    model_name text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        DELETE FROM vec.embeddings AS e
         USING ctx.lcm_embedding_profile AS p
         WHERE e.kind = 'summary' AND e.item_id = OLD.embedded_id::bigint
           AND p.identity_hash = OLD.identity_hash AND e.model = p.model_name;
        RETURN OLD;
    END IF;
    SELECT p.model_name INTO model_name FROM ctx.lcm_embedding_profile AS p WHERE p.identity_hash = NEW.identity_hash;
    IF model_name IS NULL THEN
        PERFORM ctx.lcm_raise('no embedding profile ' || NEW.identity_hash);
    END IF;
    IF vector_dims(NEW.vec) <> 1024 THEN
        PERFORM ctx.lcm_raise('summary vectors on the memory store are 1024 wide (the one embedding model); this '
                              'profile writes ' || vector_dims(NEW.vec));
    END IF;
    INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding)
    VALUES ('summary', NEW.embedded_id::bigint, public.lina_current_agent(), model_name, 1024, NEW.vec)
    ON CONFLICT (kind, item_id, model) DO UPDATE SET embedding = EXCLUDED.embedding;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_insert INSTEAD OF INSERT ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();
CREATE OR REPLACE TRIGGER lcm_embedding_vectors_delete INSTEAD OF DELETE ON ctx.lcm_embedding_vectors
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_embedding_vectors_write();
