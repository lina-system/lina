-- Personal documents (lina#308, docs/store.md "Personal documents"): a document the owner marked personal, or one
-- read from a folder carrying the marker file `.lina-personal`, is recorded with the job it belongs to. The message
-- store masks ID and account numbers in every text row a marked job writes (hermes_state_store.MessageStore.put_text,
-- agent/personal_numbers.py); the job's derived files live in one work folder per device and are deleted when the
-- job ends. Each agent sees only its own rows: the tables carry agent_id, so row-level security (lina_agent_rows)
-- applies to them when they are created.

-- A job is the session that started it and every session that continued it (a compaction's child session).
CREATE TABLE IF NOT EXISTS memory.personal_job_sessions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    session_id text NOT NULL,
    job_id text NOT NULL,
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, session_id)
);
CREATE INDEX IF NOT EXISTS personal_job_sessions_job ON memory.personal_job_sessions (agent_id, job_id);

-- One row per marked document of a job. `how`: the owner said so (`owner`, the mark_personal tool) or the document
-- sits under a folder with the marker file (`folder`).
CREATE TABLE IF NOT EXISTS memory.personal_marks (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    job_id text NOT NULL,
    path text NOT NULL,
    how text NOT NULL CHECK (how IN ('owner', 'folder')),
    marked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, job_id, path)
);

-- The run record of a marked job: work folders opened (per device), their deletion with the count of files deleted
-- and how the job ended, and every public post or third-party upload refused. `detail` holds paths, counts, tool
-- names and destinations, never document text.
CREATE TABLE IF NOT EXISTS memory.personal_job_events (
    event_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    job_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('work_folder', 'cleaned', 'refused')),
    device text NOT NULL,
    detail jsonb NOT NULL DEFAULT '{}'::jsonb,
    at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS personal_job_events_job ON memory.personal_job_events (agent_id, job_id);

-- lina_app has every table privilege in `memory` by default (services/store/initdb); a mark is added or removed,
-- never rewritten, and the run record is append-only.
REVOKE UPDATE ON memory.personal_job_sessions, memory.personal_marks FROM lina_app;
REVOKE UPDATE, DELETE, TRUNCATE ON memory.personal_job_events FROM lina_app;
