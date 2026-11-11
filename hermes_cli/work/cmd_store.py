"""Moving records in and out: import (into the configured store), migrate (a jsonl ledger into a SQLite file),
export-jsonl, freeze/thaw, run-sync."""

from __future__ import annotations

import fcntl  # windows-footgun: ok — the work record runs on the POSIX hub only
import json
import os
import re
import sys
from pathlib import Path

from hermes_cli.migrate_memory_work_rows import KINDS, canonical, key
from hermes_cli.migrate_memory_work_rows import rows as table_rows
from hermes_cli.work import ledger, store
from hermes_cli.work.common import Lock, die, iso, now, paths, warn, writer_profile
from hermes_cli.work.env import current
from hermes_cli.work.kinds import current as current_kind
from hermes_cli.work.locale import t

RUN_COLUMNS = ("id", "runner", "title", "mode", "topic", "agent_id", "goal_id", "device", "created_at", "started_at",
               "last_activity_at", "ended_at", "exit_code", "result_path", "cancelled_at", "background_since")


def read_jsonl(path: Path) -> list[tuple[int, str, dict | None]]:
    """-> (line number, text, parsed row or None) the way the jsonl reader read the file; opened read-only."""
    out = []
    with open(path, encoding="utf-8-sig") as f:
        for n, raw in enumerate(f, 1):
            text = raw.strip()
            if not text:
                continue
            try:
                row = json.loads(text)
            except ValueError:
                row = None
            out.append((n, text, row if isinstance(row, dict) and row.get("id") is not None else None))
    return out


def latest(rows) -> tuple[dict, list]:
    recs, order = {}, []
    for row in rows:
        if row.get("_meta"):
            continue
        if row["id"] not in recs:
            order.append(row["id"])
        recs[row["id"]] = current_kind(dict(row))
    return recs, order


class Source:
    """A record to import, opened read-only: a ``ledger.jsonl`` or a ``work.db`` (SQLite edition of the schema)."""

    def __init__(self, path: Path):
        self.path, self.db = path, None
        if not path.is_file():
            die(t("원본이 없다: {path}", path=path))
        with open(path, "rb") as fh:
            self.sqlite = fh.read(16) == b"SQLite format 3\x00"
        if self.sqlite:
            from hermes_cli.work import store_sqlite
            env = current()
            self.db = store_sqlite.connect(path, env.owner, env.names, readonly=True)

    def lines(self) -> list[tuple[int, str, dict | None]]:
        if self.db is None:
            return read_jsonl(self.path)
        out = []
        for eid, line in store.lines(self.db):
            try:
                row = json.loads(line)
            except ValueError:
                row = None
            out.append((eid, line, row if isinstance(row, dict) and row.get("id") is not None else None))
        return out

    def counts(self) -> dict:
        if self.db is None:
            return {}
        present = {r[0] for r in self.db.execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()}
        return {table: self.db.execute(f"SELECT count(*) FROM {table}").fetchone()[0]
                for table in store.TABLES if table in present}

    def close(self) -> None:
        if self.db is not None:
            self.db.close()


def _imported(db, lines) -> dict[int, str]:
    """Source line number -> the store's line for it: by the import marker, or, for a record written here (its own
    export), by position."""
    rows = db.execute(f"SELECT {db.jget('data', 'import_line')}, {db.jtext('data')} FROM event "
                      f"WHERE {db.jget('data', 'import_line')} IS NOT NULL").fetchall()
    if rows:
        return {int(n): json.loads(data)["ledger"] for n, data in rows}
    own = [line for _, line in store.lines(db)]
    numbers = [n for n, _, row in lines if row is not None]
    return dict(zip(numbers, own))


def _info(row: dict) -> dict:
    info = ledger.describe(row)
    at = store.utc(row.get("ts")) or store.utc(row.get("created"))
    return dict(info, at=at, link_at={(row.get("id"), link): at for link in row.get("links") or []})


def _import_line(db, n: int, text: str, row: dict, *, stored_event: dict | None = None) -> None:
    # A SQLite source has authoritative projections and event metadata (answers,
    # thread parents, native IDs). Its selected-row importer supplies those intact.
    if stored_event is not None:
        store.upsert(db, "event", stored_event["id"],
                     {c: v for c, v in stored_event.items() if c != "id"})
        return
    data_line = text
    if row.get("_meta"):
        eid = store.insert_event(db, row, data_line, "import")
    else:
        prev = store.latest(db, row.get("id"))
        info = _info(row)
        info = dict(info, created_by=row.get("writer") if prev is None and row.get("op") == "add" else None)
        store.project_item(db, row, info, prev)
        eid = store.insert_event(db, row, data_line, "import")
    data = {"op": row.get("op"), "ledger": data_line, "import_line": n}
    if row.get("_meta"):
        data["meta"] = True
    db.execute("UPDATE event SET data = ? WHERE id = ?", (json.dumps(data, ensure_ascii=False), eid))


def _copy_runs(db, runs, pairs) -> int:
    for run in runs:
        store.actor(db, run[RUN_COLUMNS.index("agent_id")])
        values = dict(zip(RUN_COLUMNS, run))
        if values.get("goal_id") and not store.exists(db, "goal", values["goal_id"]):
            values["goal_id"] = None
        db.execute(f"INSERT INTO run ({', '.join(RUN_COLUMNS)}) VALUES ({', '.join('?' * len(RUN_COLUMNS))}) "
                   "ON CONFLICT (id) DO NOTHING", [values[c] for c in RUN_COLUMNS])
    copied = 0
    for run_id, todo_id, closes, added_by, added_at in pairs:
        if store.exists(db, "todo", todo_id) and store.exists(db, "run", run_id):
            store.actor(db, added_by)
            db.execute("INSERT INTO run_todo (run_id, todo_id, closes, added_by, added_at) VALUES (?, ?, ?, ?, ?) "
                       "ON CONFLICT DO NOTHING", (run_id, todo_id, closes, added_by, added_at))
            copied += 1
    return copied


def import_selected_rows(db, selected: dict) -> None:
    """Import absent, preflighted SQLite rows, preserving their authoritative projection.

    The caller owns the transaction and collision checks. Unlike JSONL replay,
    table-backed import must not rebuild relations or discard run event metadata.
    """
    for records in selected.values():
        for row in records:
            for column in ("agent_id", "created_by", "owner_id", "actor_id", "asked_by", "answered_by",
                           "added_by", "relayed_by", "closed_by_id"):
                if row.get(column):
                    store.actor(db, row[column])
    for kind in ("area", "project", "goal"):
        for row in selected[kind]:
            store.upsert(db, kind, row["id"], {c: v for c, v in row.items() if c != "id"})
    _copy_runs(db, [tuple(row[c] for c in RUN_COLUMNS) for row in selected["run"]], [])
    for kind in ("todo", "wait", "decision", "link"):
        for row in selected[kind]:
            store.upsert(db, kind, row["id"], {c: v for c, v in row.items() if c != "id"})
    _copy_runs(db, [], [tuple(row[c] for c in ("run_id", "todo_id", "closes", "added_by", "added_at"))
                       for row in selected["run_todo"]])
    for row in selected["event"]:
        data = json.loads(row["data"])
        text = data.get("ledger", "{}")
        # Event IDs are not a topological order and valid threads can cycle.
        _import_line(db, row["id"], text, json.loads(text), stored_event=dict(row, parent_id=None))
    for row in selected["event"]:
        if row["parent_id"] is not None:
            db.execute("UPDATE event SET parent_id = ? WHERE id = ?", (row["parent_id"], row["id"]))
    if db.dialect == "postgres":
        for kind in ("event", "wait", "decision", "link"):
            if selected[kind]:
                db.execute(f"SELECT setval(pg_get_serial_sequence('work.{kind}', 'id'), "
                           f"GREATEST(COALESCE((SELECT max(id) FROM {kind}), 0), 1), true)")


def _check_counts(src: Source, db, lines, skipped, scope=None) -> tuple[bool, list[str]]:
    """Rows per table, source against store; then every item's latest record and their order (what json reads);
    for a work.db, the content of the rows this import wrote (scope), so equal counts cannot hide stale rows."""
    ok, report = True, [t("표          원본      저장소  결과")]
    rows = [row for _, _, row in lines if row]
    recs, order = latest(rows)
    want = src.counts() or {
        "area": sum(1 for r in recs.values() if store.table_of(r) == "area"),
        "goal": sum(1 for r in recs.values() if store.table_of(r) == "goal"),
        "todo": sum(1 for r in recs.values() if store.table_of(r) == "todo"),
        "event": len(rows) - len(skipped)}
    got = store.counts(db)
    for table in [x for x in store.TABLES if x in want and x not in ("actor", "setting", "status_label", "outbox")]:
        same = want[table] == got[table]
        ok = ok and same
        report.append(f"{table:10} {want[table]:>7} {got[table]:>7}  " + (t("일치") if same else t("다름")))
    back, back_order = store.read_all(db)
    same = back_order == [i for i in order if i in back] and all(back.get(i) == recs[i] for i in back)
    ok = ok and same and set(back) == set(recs) - {row["id"] for row in skipped}
    report.append(t("최신 기록 {n}개: {result}", n=len(back), result=t("일치") if same else t("다름")))
    if scope is not None:
        want_rows, touched = scope
        got_rows = {kind: {key(kind, row): row for row in records
                           if key(kind, row) in want_rows[kind] or _belongs(kind, row, touched)}
                    for kind, records in table_rows(db).items()}
        same = all({k: canonical(db, kind, r) for k, r in want_rows[kind].items()}
                   == {k: canonical(db, kind, r) for k, r in got_rows[kind].items()} for kind in KINDS)
        ok = ok and same
        report.append(t("표 내용 {n}행: {result}", n=sum(map(len, want_rows.values())),
                        result=t("일치") if same else t("다름")))
    return ok, report


def _import_new(db, lines, done: dict) -> tuple[int, list]:
    added, skipped = 0, []
    for n, text, row in lines:
        if row is None:
            continue
        if n in done:
            if done[n] != text:
                die(t("저장소의 {n}번째 줄이 원본과 다르다. 다른 기록이 이미 들어 있다", n=n), 1)
            continue
        try:
            _import_line(db, n, text, row)
            added += 1
        except LookupError:
            skipped.append(row)
    return added, skipped


def _event_data(value) -> dict:
    return json.loads(value) if isinstance(value, str) else value


IDENTIFIER = re.compile(r"[a-z_][a-z0-9_]*")


def _check_columns(db, src: Source) -> None:
    """Every source table has exactly the store's columns, each a plain identifier: its names become the store's
    SQL (and psql input on Postgres), so anything else stops the import before a write."""
    for kind in (k for k in KINDS if k in store.TABLES):
        got, want = src.db.columns(kind), db.columns(kind)
        if set(got) != set(want) or not all(IDENTIFIER.fullmatch(c) for c in got):
            die(t("원본 work.db의 {table} 표 열이 저장소와 다르다: {columns}", table=kind,
                  columns=", ".join(repr(c) for c in sorted(set(got) ^ set(want)) or got)), 1)


def _belongs(kind: str, row: dict, touched: dict) -> bool:
    """An item's projection rows, the ones store.project_item rewrites from its latest record."""
    if kind in touched:
        return row["id"] in touched[kind]
    if kind == "run_todo":
        return row["todo_id"] in touched["todo"]
    if kind in ("wait", "decision", "link") and not (kind == "decision" and row["run_id"] is not None):
        return row["goal_id"] in touched["goal"] or row["todo_id"] in touched["todo"]
    return False


def _import_tables(db, src: Source) -> tuple[int, int, tuple]:
    """A work.db source: copy its absent rows as stored. Replaying its lines would rebuild projects and links from
    today's Desk paths and repo settings, not the source's (lina#480). An event already in the store must match;
    an item that gets new events here takes the source's projection rows, since its edits live there. -> (new
    events, new run/to-do pairs, the rows to check)."""
    source, present = table_rows(src.db), table_rows(db)
    have = {kind: {key(kind, row): row for row in present[kind]} for kind in KINDS}
    for row in source["event"]:
        old = have["event"].get(key("event", row))
        if old is not None and _event_data(old["data"]) != _event_data(row["data"]):
            die(t("저장소의 {n}번째 줄이 원본과 다르다. 다른 기록이 이미 들어 있다", n=row["id"]), 1)
    new_events = [row for row in source["event"] if key("event", row) not in have["event"]]
    touched = {kind: {row[kind + "_id"] for row in new_events if row[kind + "_id"] is not None}
               for kind in ("area", "goal", "todo")}
    touched["project"] = {row["project_id"] for row in source["goal"]
                          if row["id"] in touched["goal"] and row["project_id"] is not None}
    for kind in ("wait", "decision", "link"):
        stale = [row["id"] for row in present[kind] if _belongs(kind, row, touched)]
        for i in stale:
            db.execute(f"DELETE FROM {kind} WHERE id = ?", (i,))
    for todo_id in touched["todo"]:
        db.execute("DELETE FROM run_todo WHERE todo_id = ?", (todo_id,))
    absent = {kind: [row for row in source[kind] if key(kind, row) not in have[kind]] for kind in KINDS}
    scope = {kind: {key(kind, row): row for row in source[kind]
                    if key(kind, row) not in have[kind] or _belongs(kind, row, touched)} for kind in KINDS}
    import_selected_rows(db, {kind: list(scope[kind].values()) for kind in KINDS})
    return len(absent["event"]), len(absent["run_todo"]), (scope, touched)


def cmd_import(a) -> None:
    """Copy a record into the configured store: idempotent (a line already imported is never written twice), the
    source opened read-only, then a count check per table and a compare of every item's latest record."""
    src = Source(Path(a.from_).expanduser())
    try:
        lines = src.lines()
        with Lock():
            db = ledger.db()
            if src.db is not None:
                _check_columns(db, src)
            with store.transaction(db):
                scope = None
                if src.db is None:
                    added, skipped = _import_new(db, lines, _imported(db, lines))
                    copied = 0
                else:
                    (added, copied, scope), skipped = _import_tables(db, src), []
                ok, report = _check_counts(src, db, lines, skipped, scope)
                if not ok:
                    print("\n".join(report))
                    raise SystemExit(t("가져온 기록이 원본과 다르다. 아무것도 바꾸지 않았다"))
    finally:
        src.close()
    print("\n".join(report))
    print(t("가져옴: 새 줄 {added}개 · 붙을 항목이 없어 건너뛴 줄 {skipped}개 · 작업-할 일 {pairs}개 · 저장소 {backend}",
            added=added, skipped=len(skipped), pairs=copied, backend=ledger.backend()))


def cmd_migrate(a) -> None:
    """A jsonl ledger -> a new SQLite work.db file (the same import, into a file of its own)."""
    from hermes_cli.work import store_sqlite
    src, dst = Path(a.from_).expanduser(), Path(a.to).expanduser()
    building = dst.with_name(dst.name + ".building")
    for p in (building, Path(str(building) + "-wal"), Path(str(building) + "-shm")):
        p.unlink(missing_ok=True)
    env = current()
    db = store_sqlite.connect(building, env.owner, env.names, create=True)
    try:
        source = Source(src)
        lines = source.lines()
        with store.transaction(db):
            store.seed(db, {})
            _, skipped = _import_new(db, lines, {})
            ok, report = _check_counts(source, db, lines, skipped)
        db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
    finally:
        db.close()
    print("\n".join(report))
    if not ok:
        building.unlink(missing_ok=True)
        die(t("옮긴 검사가 원본과 다르다. 아무것도 바꾸지 않았다"), 1)
    building.replace(dst)
    print(t("옮김: {dst} (건너뛴 줄 {n}개)", dst=dst, n=len(skipped)))


def cmd_export_jsonl(a) -> None:
    """The store (or a work.db) -> ledger.jsonl in the old format (the way back)."""
    db = Source(Path(a.from_).expanduser()).db if a.from_ else ledger.db()
    if db is None:
        die(t("--from은 work.db 파일이다"))
    out = [line + "\n" for _, line in store.lines(db)]
    if not a.to:
        sys.stdout.write("".join(out))
        return
    dst = Path(a.to).expanduser()
    tmp = dst.with_name(dst.name + ".tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        f.write("".join(out))
        f.flush()
        os.fsync(f.fileno())
    tmp.replace(dst)
    print(t("내보냄: {n}줄 -> {dst}", n=len(out), dst=dst))


def cmd_freeze(a) -> None:
    """Writers (every Lock()) wait from now until `work thaw`; returns once the write in flight has finished."""
    p = paths()
    p["dir"].mkdir(parents=True, exist_ok=True)
    p["freeze"].write_text(json.dumps({"at": iso(), "by": writer_profile(), "why": a.why}, ensure_ascii=False) + "\n",
                           encoding="utf-8")
    with open(p["lock"], "w", encoding="utf-8") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        fcntl.flock(f, fcntl.LOCK_UN)
    print(t("잠금: 쓰기는 work thaw까지 기다린다 ({path})", path=p["freeze"]))


def cmd_thaw(a) -> None:
    paths()["freeze"].unlink(missing_ok=True)
    print(t("잠금 풀림"))


def cmd_run_sync(a) -> None:
    """Runs from `lina worker` job files (and an optional session queue) into run and run_todo."""
    from hermes_cli.work.run import worker_jobs
    from hermes_cli.work.sources import sessions_index
    jobs = worker_jobs(a.omo_jobs)
    sessions = list(sessions_index()[1].values())
    with Lock():
        db = ledger.db()
        with store.transaction(db):
            missing = store.sync_runs(db, jobs, sessions, store.utc(now()))
    print(t("작업 {jobs}개·세션 {sessions}개 맞춤", jobs=len(jobs), sessions=len(sessions))
          + (t(" · 기록에 없는 할 일 {n}개는 뺌", n=len(missing)) if missing else ""))
    if missing:
        warn(", ".join(f"{r}->{td}" for r, td in missing[:20]))
