-- v_todo reads v_run and wait once (lina#595). 0004's v_todo found each to-do's latest run and latest open wait
-- with a subquery per to-do, which rebuilt v_run for every to-do. Here a window over all of them picks the same
-- rows in one pass; columns and values are 0004's. On a tie (same created_at, same since) the smaller id wins:
-- 0004's ORDER BY ... LIMIT 1 left a tie to the plan. Rollback: rerun 0004's CREATE OR REPLACE VIEW v_todo.
SET search_path TO work, public;

CREATE OR REPLACE VIEW v_todo WITH (security_invoker = true) AS
WITH todo_runs AS (
    SELECT rt.todo_id, vr.status, vr.reason,
        row_number() OVER (PARTITION BY rt.todo_id
                           ORDER BY vr.status = 'dropped', vr.created_at DESC, vr.id) AS n
    FROM run_todo rt JOIN v_run vr ON vr.id = rt.run_id
), runs AS (
    SELECT todo_id,
        count(CASE WHEN status <> 'dropped' THEN 1 END) AS run_count,
        bool_or(status = 'in_progress') AS any_running,
        bool_or(status = 'waiting') AS any_waiting,
        max(CASE WHEN n = 1 AND status <> 'dropped' THEN status END) AS last_status,
        max(CASE WHEN n = 1 AND status <> 'dropped' THEN reason END) AS last_reason
    FROM todo_runs
    GROUP BY todo_id
), waits AS (
    SELECT todo_id, reason FROM (
        SELECT todo_id, reason, row_number() OVER (PARTITION BY todo_id ORDER BY since DESC, id) AS n
        FROM wait WHERE released_at IS NULL AND todo_id IS NOT NULL
    ) w
    WHERE n = 1
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
