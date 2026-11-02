-- The LINA `work` schema, SQLite edition (lina#36). The Postgres edition is
-- services/store/migrations/0001_work.sql: same tables, same column names, same relations and the same
-- status views. Type mapping: enum -> TEXT with the values in CHECK; identity -> INTEGER PRIMARY KEY;
-- timestamptz -> UTC ISO-8601 TEXT; interval -> TEXT in Postgres interval syntax ('10 minutes'); jsonb -> JSON
-- TEXT; boolean -> 0/1. todo.goal_id may be NULL (an owner's own to-do without a goal, and memos = inbox to-dos).
-- outbox: a record change and its hand-off to `lina worker` are written in one transaction; the worker call reads
-- the outbox row and marks it done (hermes_cli/work/run.py).

CREATE TABLE IF NOT EXISTS actor (
    id TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('human', 'agent')),
    display_name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS setting (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS area (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    para_path TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    created_by TEXT NOT NULL REFERENCES actor (id),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    archived_at TEXT
);

CREATE TABLE IF NOT EXISTS project (
    id TEXT PRIMARY KEY,
    area_id TEXT NOT NULL REFERENCES area (id),
    title TEXT NOT NULL,
    para_path TEXT NOT NULL UNIQUE,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    created_by TEXT NOT NULL REFERENCES actor (id),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    archived_at TEXT
);

CREATE TABLE IF NOT EXISTS goal (
    id TEXT PRIMARY KEY,
    area_id TEXT NOT NULL REFERENCES area (id),
    project_id TEXT REFERENCES project (id),
    title TEXT NOT NULL,
    done_criteria TEXT,
    check_cmd TEXT,
    why TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    owner_id TEXT REFERENCES actor (id),
    created_by TEXT NOT NULL REFERENCES actor (id),
    created_at TEXT NOT NULL,
    due_date TEXT,
    promise INTEGER NOT NULL DEFAULT 0 CHECK (promise IN (0, 1)),
    stall_after TEXT,
    repeat_every TEXT,
    closed_at TEXT,
    close_kind TEXT CHECK (close_kind IN ('done', 'dropped')),
    closed_by_kind TEXT CHECK (closed_by_kind IN ('check', 'assigned_agent', 'other_agent', 'owner')),
    closed_by_id TEXT REFERENCES actor (id),
    CHECK ((closed_at IS NULL) = (close_kind IS NULL))
);

CREATE TABLE IF NOT EXISTS run (
    id TEXT PRIMARY KEY,
    runner TEXT NOT NULL,
    title TEXT NOT NULL,
    mode TEXT,
    topic TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    goal_id TEXT REFERENCES goal (id),
    device TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    last_activity_at TEXT,
    ended_at TEXT,
    exit_code INTEGER,
    result_path TEXT,
    cancelled_at TEXT,
    background_since TEXT
);
CREATE INDEX IF NOT EXISTS run_agent_id ON run (agent_id);
CREATE INDEX IF NOT EXISTS run_ended_at ON run (ended_at) WHERE ended_at IS NULL;

CREATE TABLE IF NOT EXISTS todo (
    id TEXT PRIMARY KEY,
    goal_id TEXT REFERENCES goal (id),
    title TEXT NOT NULL,
    done_criteria TEXT,
    check_cmd TEXT,
    why TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    owner_id TEXT REFERENCES actor (id),
    created_by TEXT NOT NULL REFERENCES actor (id),
    relayed_by TEXT REFERENCES actor (id),
    created_at TEXT NOT NULL,
    due_date TEXT,
    promise INTEGER NOT NULL DEFAULT 0 CHECK (promise IN (0, 1)),
    stall_after TEXT,
    repeat_every TEXT,
    closed_at TEXT,
    close_kind TEXT CHECK (close_kind IN ('done', 'dropped')),
    closed_by_kind TEXT CHECK (closed_by_kind IN ('check', 'assigned_agent', 'other_agent', 'owner')),
    closed_by_id TEXT REFERENCES actor (id),
    close_run_id TEXT REFERENCES run (id),
    CHECK ((closed_at IS NULL) = (close_kind IS NULL))
);
CREATE INDEX IF NOT EXISTS todo_goal_id ON todo (goal_id);
CREATE INDEX IF NOT EXISTS todo_owner_id ON todo (owner_id) WHERE closed_at IS NULL;

CREATE TABLE IF NOT EXISTS run_todo (
    run_id TEXT NOT NULL REFERENCES run (id) ON DELETE CASCADE,
    todo_id TEXT NOT NULL REFERENCES todo (id),
    closes INTEGER NOT NULL DEFAULT 1 CHECK (closes IN (0, 1)),
    added_by TEXT NOT NULL REFERENCES actor (id),
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    PRIMARY KEY (run_id, todo_id)
);
CREATE INDEX IF NOT EXISTS run_todo_todo_id ON run_todo (todo_id);

CREATE TABLE IF NOT EXISTS wait (
    id INTEGER PRIMARY KEY,
    goal_id TEXT REFERENCES goal (id),
    todo_id TEXT REFERENCES todo (id),
    run_id TEXT REFERENCES run (id),
    reason TEXT NOT NULL CHECK (reason IN ('decision', 'feedback', 'review', 'hold', 'blocked_by_item')),
    on_text TEXT NOT NULL,
    on_item TEXT,
    since TEXT NOT NULL,
    released_at TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    CHECK ((goal_id IS NOT NULL) + (todo_id IS NOT NULL) + (run_id IS NOT NULL) = 1)
);
CREATE INDEX IF NOT EXISTS wait_todo_id ON wait (todo_id) WHERE released_at IS NULL;

CREATE TABLE IF NOT EXISTS decision (
    id INTEGER PRIMARY KEY,
    goal_id TEXT REFERENCES goal (id),
    todo_id TEXT REFERENCES todo (id),
    run_id TEXT REFERENCES run (id),
    kind TEXT NOT NULL CHECK (kind IN ('question', 'request')),
    prompt TEXT NOT NULL,
    options TEXT NOT NULL DEFAULT '[]',
    asked_by TEXT NOT NULL REFERENCES actor (id),
    asked_at TEXT NOT NULL,
    answer TEXT,
    answered_by TEXT REFERENCES actor (id),
    answered_at TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    CHECK ((goal_id IS NOT NULL) + (todo_id IS NOT NULL) + (run_id IS NOT NULL) = 1),
    CHECK ((answer IS NULL) = (answered_at IS NULL))
);
CREATE INDEX IF NOT EXISTS decision_todo_id ON decision (todo_id) WHERE answered_at IS NULL;

CREATE TABLE IF NOT EXISTS link (
    id INTEGER PRIMARY KEY,
    goal_id TEXT REFERENCES goal (id),
    todo_id TEXT REFERENCES todo (id),
    run_id TEXT REFERENCES run (id),
    kind TEXT NOT NULL CHECK (kind IN ('gh_issue', 'gh_pr', 'gh_milestone', 'gh_ref', 'todoist', 'desk', 'kanban', 'url')),
    target TEXT NOT NULL,
    label TEXT,
    closes INTEGER NOT NULL DEFAULT 0 CHECK (closes IN (0, 1)),
    added_by TEXT NOT NULL REFERENCES actor (id),
    added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    agent_id TEXT NOT NULL REFERENCES actor (id),
    CHECK ((goal_id IS NOT NULL) + (todo_id IS NOT NULL) + (run_id IS NOT NULL) = 1)
);
CREATE UNIQUE INDEX IF NOT EXISTS link_target
    ON link (coalesce(goal_id, ''), coalesce(todo_id, ''), coalesce(run_id, ''), kind, target);

-- One row per ledger line, in line order: data = {"op", "ledger": the line verbatim, "meta"?}. Run events
-- (run_id set) carry data = {"op", ...} without "ledger": they are hand-off history, not ledger lines.
CREATE TABLE IF NOT EXISTS event (
    id INTEGER PRIMARY KEY,
    at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    actor_id TEXT NOT NULL REFERENCES actor (id),
    area_id TEXT REFERENCES area (id),
    goal_id TEXT REFERENCES goal (id),
    todo_id TEXT REFERENCES todo (id),
    run_id TEXT REFERENCES run (id),
    kind TEXT NOT NULL CHECK (kind IN ('created', 'edited', 'said', 'note', 'evidence', 'comment', 'reply', 'answer',
        'wait', 'release', 'status', 'run_started', 'run_activity', 'run_ended', 'run_cancelled', 'link_added',
        'run_todo_added', 'run_todo_removed', 'imported')),
    body TEXT,
    data TEXT NOT NULL DEFAULT '{}',
    parent_id INTEGER REFERENCES event (id),
    thread_state TEXT CHECK (thread_state IN ('resolved', 'applied', 'needs_answer', 'wont_apply')),
    via TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id),
    CHECK ((area_id IS NOT NULL) + (goal_id IS NOT NULL) + (todo_id IS NOT NULL) + (run_id IS NOT NULL) = 1)
);
CREATE INDEX IF NOT EXISTS event_todo_at ON event (todo_id, at DESC);
CREATE INDEX IF NOT EXISTS event_goal_at ON event (goal_id, at DESC);
CREATE INDEX IF NOT EXISTS event_run_at ON event (run_id, at DESC);
CREATE INDEX IF NOT EXISTS event_area ON event (area_id);

CREATE TABLE IF NOT EXISTS status_label (
    code TEXT PRIMARY KEY,
    label TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS outbox (
    id INTEGER PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES run (id),
    op TEXT NOT NULL CHECK (op IN ('submit', 'send', 'accept', 'review', 'retry', 'cancel')),
    payload TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT,
    done_at TEXT,
    agent_id TEXT NOT NULL REFERENCES actor (id)
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (id) WHERE done_at IS NULL;

-- Status: computed here only (v_run -> v_todo -> v_goal), as in the Postgres edition, in SQLite syntax.
-- now() - x > interval  ==  datetime(x, '+' || interval) < datetime('now').
CREATE VIEW IF NOT EXISTS v_run AS
WITH r AS (
    SELECT run.*,
        EXISTS (SELECT 1 FROM decision d WHERE d.run_id = run.id AND d.answered_at IS NULL) AS asking,
        (background_since IS NOT NULL AND datetime(background_since, '+' ||
            (SELECT value FROM setting WHERE key = 'run_background_limit')) < datetime('now')) AS over_background,
        (background_since IS NULL AND datetime(coalesce(last_activity_at, started_at), '+' ||
            (SELECT value FROM setting WHERE key = 'run_no_response_after')) < datetime('now')) AS silent
    FROM run
)
SELECT id, runner, title, mode, topic, agent_id, goal_id, device, created_at, started_at, last_activity_at, ended_at,
    exit_code, result_path, cancelled_at, background_since,
    CASE
        WHEN cancelled_at IS NOT NULL THEN 'dropped'
        WHEN ended_at IS NOT NULL AND exit_code = 0 AND result_path IS NOT NULL THEN 'done'
        WHEN ended_at IS NOT NULL THEN 'stopped'
        WHEN asking THEN 'waiting'
        WHEN started_at IS NULL THEN 'planned'
        WHEN over_background OR silent THEN 'stopped'
        ELSE 'in_progress'
    END AS status,
    CASE
        WHEN cancelled_at IS NOT NULL THEN NULL
        WHEN ended_at IS NOT NULL AND exit_code = 0 AND result_path IS NULL THEN 'result_missing'
        WHEN ended_at IS NOT NULL AND coalesce(exit_code, 1) <> 0 THEN 'failed'
        WHEN ended_at IS NULL AND started_at IS NOT NULL AND asking THEN 'decision'
        WHEN ended_at IS NULL AND started_at IS NOT NULL AND (over_background OR silent) THEN 'no_response'
    END AS reason
FROM r;

CREATE VIEW IF NOT EXISTS v_todo AS
WITH runs AS (
    SELECT rt.todo_id,
        count(CASE WHEN vr.status <> 'dropped' THEN 1 END) AS run_count,
        max(vr.status = 'in_progress') AS any_running,
        max(vr.status = 'waiting') AS any_waiting,
        (SELECT v.status FROM run_todo x JOIN v_run v ON v.id = x.run_id
         WHERE x.todo_id = rt.todo_id AND v.status <> 'dropped' ORDER BY v.created_at DESC LIMIT 1) AS last_status,
        (SELECT v.reason FROM run_todo x JOIN v_run v ON v.id = x.run_id
         WHERE x.todo_id = rt.todo_id AND v.status <> 'dropped' ORDER BY v.created_at DESC LIMIT 1) AS last_reason
    FROM run_todo rt JOIN v_run vr ON vr.id = rt.run_id
    GROUP BY rt.todo_id
), waits AS (
    SELECT w.todo_id, (SELECT x.reason FROM wait x WHERE x.todo_id = w.todo_id AND x.released_at IS NULL
                       ORDER BY x.since DESC LIMIT 1) AS reason
    FROM wait w WHERE w.released_at IS NULL AND w.todo_id IS NOT NULL GROUP BY w.todo_id
), asks AS (
    SELECT todo_id, count(*) AS n FROM decision WHERE answered_at IS NULL AND todo_id IS NOT NULL GROUP BY todo_id
), last_act AS (
    SELECT todo_id, max(at) AS at FROM event WHERE todo_id IS NOT NULL GROUP BY todo_id
)
SELECT t.*,
    CASE
        WHEN t.close_kind = 'done' THEN 'done'
        WHEN t.close_kind = 'dropped' THEN 'dropped'
        WHEN r.any_running THEN 'in_progress'
        WHEN r.any_waiting OR w.reason IS NOT NULL OR a.n > 0 THEN 'waiting'
        WHEN r.last_status = 'stopped' THEN 'stopped'
        WHEN r.last_status = 'done' THEN 'waiting'
        ELSE 'planned'
    END AS status,
    CASE
        WHEN t.closed_at IS NOT NULL THEN NULL
        WHEN r.any_running THEN NULL
        WHEN a.n > 0 THEN 'decision'
        WHEN w.reason IS NOT NULL THEN w.reason
        WHEN r.any_waiting THEN 'decision'
        WHEN r.last_status = 'stopped' THEN r.last_reason
        WHEN r.last_status = 'done' THEN 'review'
    END AS reason,
    coalesce(r.run_count, 0) AS run_count,
    la.at AS last_activity_at
FROM todo t
LEFT JOIN runs r ON r.todo_id = t.id
LEFT JOIN waits w ON w.todo_id = t.id
LEFT JOIN asks a ON a.todo_id = t.id
LEFT JOIN last_act la ON la.todo_id = t.id;

CREATE VIEW IF NOT EXISTS v_goal AS
SELECT g.*,
    count(CASE WHEN vt.status = 'done' THEN 1 END) AS todos_done,
    count(CASE WHEN vt.status <> 'dropped' THEN 1 END) AS todos_total,
    CASE
        WHEN g.close_kind = 'done' THEN 'done'
        WHEN g.close_kind = 'dropped' THEN 'dropped'
        WHEN max(vt.status = 'in_progress') THEN 'in_progress'
        WHEN max(vt.status = 'stopped') THEN 'stopped'
        WHEN max(vt.status = 'waiting') THEN 'waiting'
        WHEN sum(vt.status <> 'dropped') > 0 AND sum(vt.status NOT IN ('done', 'dropped')) = 0 THEN 'waiting'
        ELSE 'planned'
    END AS status
FROM goal g
LEFT JOIN v_todo vt ON vt.goal_id = g.id
GROUP BY g.id;
