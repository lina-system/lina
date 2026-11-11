"""The work record in the tables of the LINA ``work`` schema, on either backend (lina#36).

``event`` keeps one row per ledger line with the line itself in ``data.ledger``, so the latest line of an id gives
the record dict the jsonl ledger gave, and ``export-jsonl`` writes the old file back. area/project/goal/todo/link/
run_todo/wait/decision are a projection of each item's latest record, rewritten in the same transaction.

A backend connection (``store_sqlite.SqliteDb``, ``store_pg.PgDb``) offers ``execute(sql, params)`` with ``?``
placeholders returning a cursor (``fetchone``/``fetchall``/iteration), ``begin``/``commit``/``rollback``,
``columns(table)``, ``jget(column, key)`` (SQL for a JSON field as text), ``jtext(column)`` (a JSON column as text)
and the names of the record's people: ``human`` (the owner's actor id) and ``names`` (actor id -> display name).
"""

from __future__ import annotations

import datetime
import json
import re
from contextlib import contextmanager

from hermes_cli.work.kinds import OLD_TODO, TODO, current

TABLE = {"영역": "area", "목표": "goal", TODO: "todo", OLD_TODO: "todo", "메모": "todo"}
PREFIX_TABLE = {"A": "area", "G": "goal", "T": "todo", "M": "todo"}
ITEM_ID = "COALESCE(area_id, goal_id, todo_id)"
TABLES = ("actor", "setting", "status_label", "area", "project", "goal", "todo", "run", "run_todo", "wait",
          "decision", "link", "event", "outbox")
EPOCH = "1970-01-01T00:00:00Z"
OP_KIND = {"add": "created", "edit": "edited", "archive": "edited", "unlink": "edited", "seed-writer": "edited",
           "quote": "said", "basis": "said", "note": "note", "evidence": "evidence", "comment": "comment",
           "reply": "reply", "wait": "wait", "wait_restore": "wait", "wait_release": "release", "link": "link_added",
           "done": "status", "drop": "status", "owner_done": "status", "owner_undo": "status", "reopen": "status",
           "undo": "status", "confirm": "status", "stage": "status"}
SETTINGS = (("run_no_response_after", "10 minutes"), ("run_background_limit", "3 hours"))
STATUS_CODES = ("planned", "in_progress", "waiting", "stopped", "done", "decision", "feedback", "review", "hold",
                "blocked_by_item", "no_response", "failed", "result_missing")
LINK_KIND = {"gh": "gh_ref", "ms": "gh_milestone", "todoist": "todoist", "desk": "desk", "kanban": "kanban"}
NOT_A_LINK = ("omo", "dv", "session")  # omo: is run_todo; dv: and session: point at retired things
# What a closed record's done_by says (the CLI writes these words in the record's locale).
CHECK_WORDS = ("검사", "check")
ASSIGNED_WORDS = ("맡은 봇", "assigned agent")


@contextmanager
def transaction(db):
    db.begin()
    try:
        yield db
    except BaseException:
        db.rollback()
        raise
    db.commit()


def seed(db, labels: dict[str, str]) -> None:
    """The settings and status labels a new store starts with (idempotent)."""
    for key, value in SETTINGS:
        db.execute("INSERT INTO setting (key, value) VALUES (?, ?) ON CONFLICT (key) DO NOTHING", (key, value))
    for code in STATUS_CODES:
        db.execute("INSERT INTO status_label (code, label) VALUES (?, ?) ON CONFLICT (code) DO UPDATE SET "
                   "label = excluded.label", (code, labels.get(code, code)))


def utc(ts):
    if ts in (None, "", 0):
        return None
    try:
        return datetime.datetime.fromtimestamp(float(ts), datetime.UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
    except (TypeError, ValueError, OverflowError, OSError):
        return None


def table_of(row):
    return TABLE.get(row.get("kind")) or PREFIX_TABLE.get(str(row.get("id") or "")[:1])


def kind_of(row):
    return OP_KIND.get(str(row.get("op") or ""), "edited")


def interval_of(value, unit="hours"):
    """'1d' -> '1 day', '7d' -> '7 days', 24.0 -> '24 hours' (Postgres interval input); anything else -> None,
    so both backends hold the same projection (the record line keeps the original value)."""
    if value in (None, ""):
        return None
    if isinstance(value, (int, float)):
        return f"{value:g} {unit}"
    m = re.fullmatch(r"\s*(\d+)\s*([dhwm])\s*", str(value))
    if not m:
        return None
    n, u = int(m.group(1)), {"d": "day", "h": "hour", "w": "week", "m": "minute"}[m.group(2)]
    return f"{n} {u}" + ("s" if n != 1 else "")


# ---------------------------------------------------------------- reads
def _ledger(data: str) -> dict:
    return current(json.loads(json.loads(data)["ledger"]))


def read_all(db):
    """-> (recs, order) exactly as the jsonl reader: latest record per id, ids in first-seen order."""
    recs, order = {}, []
    rows = db.execute(f"""SELECT {db.jtext('e.data')} FROM event e JOIN (
        SELECT {ITEM_ID} AS item, max(id) AS last, min(id) AS first FROM event
        WHERE run_id IS NULL AND {db.jget('data', 'meta')} IS NULL GROUP BY {ITEM_ID}) x ON e.id = x.last
        ORDER BY x.first""")
    for (data,) in rows.fetchall():
        r = _ledger(data)
        order.append(r.get("id"))
        recs[r.get("id")] = r
    return recs, order


def rows(db, item=None, ops=None):
    """Ledger lines in file order (records and bookkeeping lines), optionally one item's or some ops'."""
    where, args = ["run_id IS NULL"], []
    if item is not None:
        where.append(f"{ITEM_ID} = ?")
        args.append(item)
    if ops:
        where.append(f"{db.jget('data', 'op')} IN (" + ",".join("?" * len(ops)) + ")")
        args.extend(ops)
    sql = f"SELECT {db.jtext('data')} FROM event WHERE " + " AND ".join(where) + " ORDER BY id"
    return [_ledger(data) for (data,) in db.execute(sql, args).fetchall()]


def lines(db):
    """(event id, ledger line) in order: the jsonl file again."""
    out = db.execute(f"SELECT id, {db.jtext('data')} FROM event WHERE run_id IS NULL ORDER BY id").fetchall()
    return [(eid, json.loads(data)["ledger"]) for eid, data in out]


# ---------------------------------------------------------------- writes
def actor(db, who):
    who = str(who or "").strip() or "unknown"
    db.execute("INSERT INTO actor (id, kind, display_name) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING",
               (who, "human" if who == db.human else "agent", db.names.get(who, who)))
    return who


def exists(db, table, i):
    return bool(i) and db.execute(f"SELECT 1 FROM {table} WHERE id = ?", (i,)).fetchone() is not None


def closed_by(db, r):
    """-> (closed_by_kind, closed_by_id) from done_by ('검사', '확인(<owner>)', '확인(<agent>, 맡은 봇)')."""
    by = str(r.get("done_by") or "")
    if r.get("state") != "done" or not by:
        return None, None
    if by in CHECK_WORDS:
        return "check", None
    m = re.search(r"\(([^,)]+)", by)
    name = m.group(1).strip() if m else ""
    who = {v: k for k, v in db.names.items()}.get(name, name) if m else None
    if who == db.human:
        return "owner", db.human
    if any(word in by for word in ASSIGNED_WORDS) or (who and who == r.get("by")):
        return "assigned_agent", who
    return "other_agent", who


def link_of(spec, gh_repo):
    """-> (kind, target) of a link row, or None for omo:/dv:/session: (not links in the schema)."""
    prefix, _, rest = str(spec).partition(":")
    if prefix in NOT_A_LINK:
        return None
    if prefix == "gh" and "#" in rest:
        repo, n = rest.split("#", 1)
        try:
            owner, name = gh_repo(repo)
            return "gh_ref", f"{owner}/{name}#{n}"
        except (ValueError, IndexError):
            return "gh_ref", rest
    if prefix in LINK_KIND:
        return LINK_KIND[prefix], rest
    return "url", str(spec)


def omo_runs(r):
    return [str(link)[4:] for link in r.get("links") or [] if str(link).startswith("omo:")]


def ensure_project(db, path, area_id, who, at):
    row = db.execute("SELECT id FROM project WHERE para_path = ?", (path,)).fetchone()
    if row:
        return row[0]
    n = db.execute("SELECT COALESCE(max(CAST(substr(id, 2) AS INTEGER)), 0) + 1 FROM project").fetchone()[0]
    db.execute("INSERT INTO project (id, area_id, title, para_path, agent_id, created_by, created_at) "
               "VALUES (?, ?, ?, ?, ?, ?, ?)",
               (f"P{n}", area_id, path.rstrip("/").rsplit("/", 1)[-1], path, who, who, at))
    return f"P{n}"


def ensure_run(db, run_id, who, at, goal_id=None, runner="omo"):
    db.execute("INSERT INTO run (id, runner, title, agent_id, goal_id, created_at) VALUES (?, ?, ?, ?, ?, ?) "
               "ON CONFLICT (id) DO NOTHING", (run_id, runner, run_id, who, goal_id, at))
    if goal_id:
        db.execute("UPDATE run SET goal_id = ? WHERE id = ? AND goal_id IS NULL", (goal_id, run_id))


def add_run_todo(db, run_id, todo_id, who, at):
    db.execute("INSERT INTO run_todo (run_id, todo_id, closes, added_by, added_at) VALUES (?, ?, ?, ?, ?) "
               "ON CONFLICT DO NOTHING", (run_id, todo_id, 1, who, at))


def upsert(db, table, i, row, keep=()):
    cols = list(row)
    update = [c for c in cols if c not in keep]
    db.execute(f"INSERT INTO {table} (id, {', '.join(cols)}) VALUES (?, {', '.join('?' * len(cols))}) "
               f"ON CONFLICT (id) DO UPDATE SET {', '.join(f'{c} = excluded.{c}' for c in update)}",
               [i] + [row[c] for c in cols])


def _closed_at(r, at):
    state = r.get("state")
    if state not in ("done", "dropped"):
        return None
    return utc(r.get("done_at") if state == "done" else r.get("dropped_at")) or utc(r.get("ts")) or at


def _item_row(db, r, created_by, created_at, closed_at):
    by = actor(db, r.get("by"))
    kind, who = closed_by(db, r)
    owner = actor(db, r["owner"]) if r.get("owner") else (db.human if r.get("by") == db.human else None)
    return {"title": r.get("title") or "", "done_criteria": r.get("done"), "check_cmd": r.get("check"),
            "why": r.get("why"), "agent_id": by, "owner_id": owner, "created_by": created_by,
            "created_at": created_at, "due_date": r.get("due") or None, "promise": 1 if r.get("promise") else 0,
            "stall_after": interval_of(r.get("stall")), "repeat_every": interval_of(r.get("every")),
            "closed_at": closed_at, "close_kind": r.get("state") if closed_at else None, "closed_by_kind": kind,
            "closed_by_id": actor(db, who) if who else None}


def _project_links(db, r, table, info, at):
    i, col, by = r.get("id"), table + "_id", actor(db, r.get("by"))
    link_at = info.get("link_at") or {}
    old = {(k, t): (b, a) for k, t, b, a in
           db.execute(f"SELECT kind, target, added_by, added_at FROM link WHERE {col} = ?", (i,)).fetchall()}
    db.execute(f"DELETE FROM link WHERE {col} = ?", (i,))
    labels, seen = info.get("labels") or [], set()
    writer = actor(db, r.get("actor") or r.get("by"))
    for n, spec in enumerate(r.get("links") or []):
        kt = link_of(spec, info["gh_repo"])
        if kt is None or kt in seen:
            continue
        seen.add(kt)
        added_by, added_at = old.get(kt, (writer, link_at.get((i, spec), at)))
        db.execute(f"INSERT INTO link ({col}, kind, target, label, closes, added_by, added_at, agent_id) "
                   "VALUES (?, ?, ?, ?, ?, ?, ?, ?)", (i, kt[0], kt[1], labels[n] if n < len(labels) else None,
                                                       0, added_by, added_at, by))
    return writer


def _project_runs(db, r, table, prev, writer, link_at, at):
    i, by, now_runs = r.get("id"), actor(db, r.get("by")), omo_runs(r)
    if table != "todo":
        for run_id in now_runs:
            ensure_run(db, run_id, by, link_at.get((i, "omo:" + run_id), at), goal_id=i)
        return
    for run_id in set(omo_runs(prev or {})) - set(now_runs):
        db.execute("DELETE FROM run_todo WHERE run_id = ? AND todo_id = ?", (run_id, i))
    for run_id in now_runs:
        first = link_at.get((i, "omo:" + run_id), at)
        ensure_run(db, run_id, by, first)
        add_run_todo(db, run_id, i, writer, first)


def _project_wait(db, r, col, at):
    i, by = r.get("id"), actor(db, r.get("by"))
    db.execute(f"DELETE FROM wait WHERE {col} = ?", (i,))
    w, rel = r.get("wait") or {}, r.get("released") or {}
    held = w or (rel.get("wait") if isinstance(rel.get("wait"), dict) else {})
    if isinstance(held, dict) and held:
        reason = "hold" if held.get("hold") else \
            "blocked_by_item" if held.get("item") and not held.get("on") else "feedback"
        db.execute(f"INSERT INTO wait ({col}, reason, on_text, on_item, since, released_at, agent_id) "
                   "VALUES (?, ?, ?, ?, ?, ?, ?)",
                   (i, reason, str(held.get("on") or held.get("item") or ""), held.get("item"),
                    utc(held.get("since")) or at, None if w else (utc(rel.get("at")) or at), by))
    db.execute(f"DELETE FROM decision WHERE {col} = ? AND run_id IS NULL", (i,))
    if r.get("decide"):
        db.execute(f"INSERT INTO decision ({col}, kind, prompt, options, asked_by, asked_at, agent_id) "
                   "VALUES (?, 'question', ?, ?, ?, ?, ?)",
                   (i, str(r["decide"]), json.dumps(r.get("decide_choices") or [], ensure_ascii=False), by, at, by))


def project_item(db, r, info, prev=None):
    """Rewrite the item's row and its link/run_todo/wait/decision rows from its latest record.
    info: {"created_by", "at" (this write), "labels", "projects", "gh_repo", "link_at": {(item, link): at}}."""
    table, i = table_of(r), r.get("id")
    at = info["at"] or EPOCH
    by = actor(db, r.get("by"))
    created_by = actor(db, info.get("created_by") or r.get("by"))
    closed_at = _closed_at(r, at)
    created_at = utc(r.get("created")) or at
    if table == "area":
        upsert(db, "area", i, {"title": r.get("title") or "", "para_path": "Areas/" + (r.get("title") or ""),
                               "agent_id": by, "created_by": created_by, "created_at": created_at,
                               "archived_at": closed_at}, keep=("created_by",))
        return
    row = _item_row(db, r, created_by, created_at, closed_at)
    parent = r.get("parent") or ""
    if table == "goal":
        if not exists(db, "area", parent):
            raise LookupError(f"goal {i} has no area {parent!r}")
        projects = info.get("projects") or []
        row.update(area_id=parent,
                   project_id=ensure_project(db, projects[0], parent, by, created_at) if projects else None)
        for p in projects[1:]:
            ensure_project(db, p, parent, by, created_at)
    else:
        row.update(goal_id=parent if exists(db, "goal", parent) else None,
                   relayed_by=actor(db, r["from"]) if r.get("from") else None)
    upsert(db, table, i, row, keep=("created_by",))
    writer = _project_links(db, r, table, info, at)
    _project_runs(db, r, table, prev, writer, info.get("link_at") or {}, at)
    _project_wait(db, r, table + "_id", at)


BODY = {"note": lambda r: ((r.get("notes") or [{}])[-1] or {}).get("text"),
        "drop": lambda r: r.get("dropped_quote"), "done": lambda r: r.get("done_evidence"),
        "owner_done": lambda r: r.get("done_evidence"), "edit": lambda r: r.get("title"),
        "quote": lambda r: r.get("quote"), "wait": lambda r: (r.get("wait") or {}).get("on"),
        "comment": lambda r: ((r.get("comments") or [{}])[-1] or {}).get("text"),
        "undo": lambda r: r.get("undo_why")}


def insert_event(db, row, line, via):
    """One ledger line -> one event row attached to its item (which must already be projected)."""
    table = table_of(row)
    if table is None or not exists(db, table, row.get("id")):
        raise LookupError(f"no item for ledger line {row.get('id')!r}")
    meta = bool(row.get("_meta"))
    body = None if meta else BODY.get(str(row.get("op")), lambda r: None)(row)
    data = {"op": row.get("op"), "ledger": line}
    if meta:
        data["meta"] = True
    agent = db.execute(f"SELECT agent_id FROM {table} WHERE id = ?", (row["id"],)).fetchone()[0]
    eid = db.execute(f"INSERT INTO event (at, actor_id, {table}_id, kind, body, data, via, agent_id) "
                     "VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id",
                     (utc(row.get("ts")) or utc(row.get("created")) or EPOCH,
                      actor(db, row.get("actor") or row.get("writer") or row.get("by")), row["id"], kind_of(row),
                      body if isinstance(body, str) else None, json.dumps(data, ensure_ascii=False), via,
                      agent)).fetchone()[0]
    if meta and row.get("op") == "seed-writer" and row.get("writer"):
        db.execute(f"UPDATE {table} SET created_by = ? WHERE id = ?", (actor(db, row["writer"]), row["id"]))
    return eid


def run_event(db, run_id, kind, who, data, body=None, at=None):
    """A hand-off event on a run (``run_started``, ``run_activity``, ``answer``, ``status``, ``run_ended``,
    ``run_cancelled``); never read back as a ledger line."""
    agent = db.execute("SELECT agent_id FROM run WHERE id = ?", (run_id,)).fetchone()[0]
    return db.execute("INSERT INTO event (at, actor_id, run_id, kind, body, data, via, agent_id) "
                      "VALUES (?, ?, ?, ?, ?, ?, 'cli', ?) RETURNING id",
                      (at or EPOCH, actor(db, who), run_id, kind, body, json.dumps(data, ensure_ascii=False),
                       agent)).fetchone()[0]


def latest(db, i):
    table = PREFIX_TABLE.get(str(i)[:1])
    if not table:
        return None
    row = db.execute(f"SELECT {db.jtext('data')} FROM event WHERE {table}_id = ? AND {db.jget('data', 'meta')} "
                     "IS NULL ORDER BY id DESC LIMIT 1", (i,)).fetchone()
    return _ledger(row[0]) if row else None


def append(db, rec, info):
    """Store one record line (the caller holds the work lock); returns the line written."""
    line = json.dumps(rec, ensure_ascii=False)
    with transaction(db):
        _append_locked(db, rec, info, line)
    return line


def _append_locked(db, rec, info, line):
    prev = latest(db, rec.get("id"))
    if prev is None and rec.get("op") == "add":
        info = dict(info, created_by=rec.get("writer"))
    elif prev is not None:
        info = dict(info, created_by=None)
    project_item(db, rec, info, prev)
    insert_event(db, rec, line, "cli")


def append_in(db, rec, info):
    """``append`` inside a transaction the caller already holds (a record change and its outbox row)."""
    line = json.dumps(rec, ensure_ascii=False)
    _append_locked(db, rec, info, line)
    return line


def append_meta(db, row):
    with transaction(db):
        insert_event(db, row, json.dumps(row, ensure_ascii=False), "cli")


def _job_facts(j, who, at):
    facts = {"title": j.get("title") or j.get("topic") or j["id"], "mode": j.get("mode"), "topic": j.get("topic"),
             "agent_id": who, "device": j.get("device") or j.get("host"), "started_at": utc(j.get("started")),
             "last_activity_at": utc(j.get("last_activity")), "ended_at": utc(j.get("ended")),
             "exit_code": j.get("rc") if isinstance(j.get("rc"), int) else None, "result_path": j.get("result"),
             "cancelled_at": utc(j.get("cancelled")) or
             (utc(j.get("ended")) or at if j.get("state") == "cancelled" else None)}
    if j.get("runner"):
        facts["runner"] = j["runner"]
    if utc(j.get("created")):
        facts["created_at"] = utc(j["created"])
    return facts


def _sync_job(db, j, at, missing):
    rid = str(j.get("id") or "").strip()
    if not rid:
        return
    who = actor(db, j.get("bot") or j.get("profile"))
    todos = [j["todo"]] if isinstance(j.get("todo"), str) and j["todo"].strip() else []
    todos += [t for t in j.get("todos") or [] if isinstance(t, str)]
    ensure_run(db, rid, who, utc(j.get("created")) or at, runner=j.get("runner") or "omo")
    facts = _job_facts(dict(j, id=rid), who, at)
    if j.get("goal") and exists(db, "goal", j["goal"]):
        facts["goal_id"] = j["goal"]
    set_ = {k: v for k, v in facts.items() if v is not None}
    db.execute(f"UPDATE run SET {', '.join(f'{k} = ?' for k in set_)} WHERE id = ?", list(set_.values()) + [rid])
    for t in dict.fromkeys(x.strip().upper() for x in todos):
        if exists(db, "todo", t):
            add_run_todo(db, rid, t, who, utc(j.get("created")) or at)
        else:
            missing.append((rid, t))


def _sync_session(db, s, at):
    sid = str(s.get("id") or "").strip()
    if not sid:
        return
    who = actor(db, s.get("profile"))
    goal = next((g for g in re.findall(r"G\d+", str(s.get("purpose") or "")) if exists(db, "goal", g)), None)
    db.execute("INSERT INTO run (id, runner, title, topic, agent_id, goal_id, created_at, started_at, ended_at, "
               "exit_code) VALUES (?, 'hub_session', ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO UPDATE SET "
               "title = excluded.title, topic = excluded.topic, agent_id = excluded.agent_id, "
               "goal_id = excluded.goal_id, started_at = excluded.started_at, ended_at = excluded.ended_at, "
               "exit_code = excluded.exit_code",
               (sid, str(s.get("purpose") or s.get("name") or sid), s.get("name"), who, goal,
                utc(s.get("created")) or utc(s.get("started")) or at, utc(s.get("started")), utc(s.get("ended")),
                s.get("rc") if isinstance(s.get("rc"), int) else None))


def sync_runs(db, jobs, sessions, at):
    """Upsert runs from worker/omo job json and session records; job ``todo`` (one id, the old shape) is read
    as [todo] next to ``todos``. -> pairs whose to-do is not in the record (left out)."""
    missing = []
    for j in jobs:
        _sync_job(db, j, at, missing)
    for s in sessions:
        _sync_session(db, s, at)
    return missing


def dump(db):
    """Every table's rows in a stable order (for rerun comparisons)."""
    out = {}
    for t in TABLES:
        cols = db.columns(t)
        out[t] = [[None if v is None else str(v) for v in r]
                  for r in db.execute(f"SELECT {', '.join(cols)} FROM {t} ORDER BY " + ", ".join(cols)).fetchall()]
    return out


def counts(db):
    """Rows per table (the import's count check)."""
    return {t: db.execute(f"SELECT count(*) FROM {t}").fetchone()[0] for t in TABLES}
