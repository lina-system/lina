-- The vectors of owner-level external messages are readable, never writable, by every agent (lina#364), as 0005
-- makes their memory.messages rows. Such a message (NULL agent_id, scope 'agents') is embedded as one vec.chunks row
-- and one 'chunk' vec.embeddings row with the shared owner '*' (hermes_state_store.SHARED_VECTOR_OWNER): not a
-- profile name, so lina_agent_rows lets no agent write it, and these SELECT-only policies let every agent read it.
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'vec.chunks'::regclass AND polname = 'lina_shared_chunks') THEN
        CREATE POLICY lina_shared_chunks ON vec.chunks
            FOR SELECT TO lina_app USING (agent_id = '*');
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'vec.embeddings'::regclass AND polname = 'lina_shared_chunk_vectors') THEN
        CREATE POLICY lina_shared_chunk_vectors ON vec.embeddings
            FOR SELECT TO lina_app USING (kind = 'chunk' AND agent_id = '*');
    END IF;
END $$;

-- `lina store reembed` wrote these rows with the owner 'None' (Python's str(None)) before this fix.
UPDATE vec.chunks AS c SET agent_id = '*'
 WHERE c.agent_id = 'None'
   AND EXISTS (SELECT 1 FROM memory.messages AS m
                WHERE m.message_id = c.message_ids[1] AND m.agent_id IS NULL AND m.scope = 'agents');
UPDATE vec.embeddings AS e SET agent_id = '*'
  FROM vec.chunks AS c
 WHERE e.kind = 'chunk' AND e.item_id = c.chunk_id AND e.agent_id = 'None' AND c.agent_id = '*';
