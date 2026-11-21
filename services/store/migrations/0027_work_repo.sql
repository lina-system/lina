-- The shared repository registry and optional membership of a to-do.
SET search_path TO work, public;

CREATE TABLE IF NOT EXISTS repo (
    id TEXT PRIMARY KEY,
    area_id TEXT NOT NULL REFERENCES area (id),
    name TEXT NOT NULL,
    forge_url TEXT NOT NULL,
    UNIQUE (area_id, name),
    CHECK (id = area_id || '/' || name)
);

CREATE TABLE IF NOT EXISTS todo_repo (
    todo_id TEXT PRIMARY KEY REFERENCES todo (id),
    repo_id TEXT NOT NULL REFERENCES repo (id)
);
CREATE INDEX IF NOT EXISTS todo_repo_repo_id ON todo_repo (repo_id);

-- Access to an assignment follows the existing task policy through the invoker's query.
ALTER TABLE todo_repo ENABLE ROW LEVEL SECURITY;
ALTER TABLE todo_repo FORCE ROW LEVEL SECURITY;

-- Like other shared work tables, repo has no agent_id or per-agent RLS.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'work.todo_repo'::regclass AND polname = 'lina_todo_repo_rows') THEN
        CREATE POLICY lina_todo_repo_rows ON todo_repo
            USING (EXISTS (SELECT 1 FROM work.todo WHERE todo.id = todo_repo.todo_id));
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'lina_app') THEN
        GRANT SELECT, INSERT, UPDATE, DELETE ON work.repo, work.todo_repo TO lina_app;
    END IF;
END $$;
