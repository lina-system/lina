-- Profile lifecycle is owner-managed; agents cannot authorize their own collection.
CREATE TABLE IF NOT EXISTS memory.agents (
    agent_id text PRIMARY KEY,
    deleted_at timestamptz
);
CREATE INDEX IF NOT EXISTS agents_deleted_at ON memory.agents (deleted_at)
    WHERE deleted_at IS NOT NULL;
REVOKE ALL ON memory.agents FROM PUBLIC, lina_app, honcho;
