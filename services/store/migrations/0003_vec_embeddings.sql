-- vec.embeddings and vec.chunks: one embedding model for every source (lina#19).
-- Applied by `lina store install|upgrade` (hermes_cli/store_migrations.py), recorded in memory.schema_migrations.
-- 0002 is left for lina#13. Row-level security on agent_id comes from the event trigger in services/store/initdb.

-- One row per embedded item and model. kind says which table item_id points at:
--   message  -> ltm.message_embeddings.id (Honcho's message chunks; one Honcho message can have several)
--   document -> ltm.documents.vec_id (Honcho's conclusions; its own id is text, vec_id is the integer one)
--   chunk    -> vec.chunks.chunk_id (memory.messages text, agent turns and external messages)
--   summary  -> ctx summaries (lina#16)
-- embedding is a 1024-wide prefix of the model's vector, L2-normalised again (Matryoshka); pgvector indexes
-- vector up to 2,000 dimensions, so one HNSW index serves every kind.
CREATE TABLE IF NOT EXISTS vec.embeddings (
    kind text NOT NULL CHECK (kind IN ('message', 'chunk', 'summary', 'document')),
    item_id bigint NOT NULL,
    agent_id text NOT NULL,
    model text NOT NULL,
    dim int NOT NULL,
    embedding vector(1024) NOT NULL,
    PRIMARY KEY (kind, item_id, model),
    CHECK (dim = vector_dims(embedding))
);

CREATE INDEX IF NOT EXISTS ix_embeddings_embedding_hnsw ON vec.embeddings
    USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64);
CREATE INDEX IF NOT EXISTS embeddings_agent_kind ON vec.embeddings (agent_id, kind, model);

-- The text that was embedded for memory.messages: one or more consecutive messages of one source.
CREATE TABLE IF NOT EXISTS vec.chunks (
    chunk_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    source text NOT NULL,
    agent_id text NOT NULL,
    message_ids bigint[] NOT NULL,
    text text NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS chunks_source_messages ON vec.chunks (source, message_ids);
CREATE INDEX IF NOT EXISTS chunks_message_ids ON vec.chunks USING gin (message_ids);

-- Where `lina store reembed` stopped, per source and model: a restart continues after last_item_id.
CREATE TABLE IF NOT EXISTS vec.reembed_progress (
    source text NOT NULL,
    model text NOT NULL,
    last_item_id bigint NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source, model)
);

-- The Honcho server writes and searches its own vectors here (kinds message and document) and nothing else.
-- Its rows carry the Honcho workspace as agent_id; it never sets lina.agent_id, so this policy is what lets it
-- see them (policies are permissive: a row passes when lina_agent_rows or this one passes).
GRANT USAGE ON SCHEMA vec TO honcho;
GRANT SELECT, INSERT, UPDATE, DELETE ON vec.embeddings TO honcho;
CREATE POLICY honcho_rows ON vec.embeddings TO honcho
    USING (kind IN ('message', 'document'))
    WITH CHECK (kind IN ('message', 'document'));
