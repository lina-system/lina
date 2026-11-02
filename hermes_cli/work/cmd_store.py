"""Moving records in and out: import (into the configured store), migrate (a jsonl ledger into a SQLite file),
export-jsonl, freeze/thaw, run-sync."""

from __future__ import annotations

import fcntl  # windows-footgun: ok — the work record runs on the POSIX hub only
import json
import os
import sys
from pathlib import Path

from hermes_cli.work import ledger, store
from hermes_cli.work.common import Lock, die, iso, now, paths, warn, writer_profile
from hermes_cli.work.env import current
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
        recs[row["id"]] = row
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

    def runs(self) -> tuple[list[tuple], list[tuple]]:
        if self.db is None:
            return [], []
        runs = self.db.execute(f"SELECT {', '.join(RUN_COLUMNS)} FROM run ORDER BY id").fetchall()
        pairs = self.db.execute("SELECT run_id, todo_id, closes, added_by, added_at FROM run_todo "
                                "ORDER BY run_id, todo_id").fetchall()
        return runs, pairs

    def counts(self) -> dict:
        if self.db is None:
            return {}
        present = {r[0] for r in self.db.execute("SELECT name FROM sqlite_master WHERE type = 'table'").fetchall()}
        return {table: self.db.execute(f"SELECT count(*) FROM {table}").fetchone()[0]
                for table in store.TABLES if table in present}

    def close(self) -> None:
        if self.db is not None:
            self.db.close()


def _info(row: dict) -> dict:
    info = ledger.describe(row)
    at = store.utc(row.get("ts")) or store.utc(row.get("created"))
    return dict(info, at=at, link_at={(row.get("id"), link): at for link in row.get("links") or []})


def _import_line(db, n: int, text: str, row: dict) -> None:
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


def _check_counts(src: Source, db, lines, skipped) -> tuple[bool, list[str]]:
    """Rows per table, source against store; then every item's latest record and their order (what json reads)."""
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


