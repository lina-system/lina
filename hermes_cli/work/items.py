"""Item helpers the commands share: ids, areas, projects, sessions, due dates, the generic read-change-append."""

from __future__ import annotations

import datetime
import re
import time

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, die, now
from hermes_cli.work.env import current
from hermes_cli.work.locale import kind as kind_word, status, t
from hermes_cli.work.writing import date_word

KINDS = {"영역": "A", "목표": "G", "할 일": "T", "메모": "M"}
KIND_IN = {"영역": "영역", "area": "영역", "목표": "목표", "goal": "목표", "할일": "할 일", "할 일": "할 일",
           "todo": "할 일", "to-do": "할 일", "메모": "메모", "memo": "메모"}
OPEN_OPS = ("note", "reopen", "link", "unlink", "evidence", "quote")


def get(recs: dict, i: str) -> dict:
    r = recs.get(i) or recs.get(i.upper())
    if not r:
        die(t("없는 항목: {id}", id=i))
    return r


def next_id(recs: dict, kind: str) -> str:
    pre = KINDS[kind]
    nums = [int(re.sub(r"\D", "", i) or 0) for i in recs if i and i.startswith(pre)]
    return pre + str(1 + max(nums or [0]))


def areas() -> list[str] | None:
    value = current().section("work").get("areas")
    return [str(a) for a in value] if isinstance(value, list) and value else None


def norm_area(v: str) -> str:
    v = re.sub(r"\s*/\s*", "/", (v or "").strip().strip("/"))
    for prefix in ("2_Areas/", "Areas/"):
        v = v.removeprefix(prefix)
    parts = [p for p in v.split("/") if p]
    allowed = areas()
    top = ({a.lower(): a for a in allowed}.get(parts[0].lower()) if allowed else parts[0]) if parts else None
    if not top or len(parts) > 2:
        die(t("영역은 {tops} 또는 한 단계 아래(예: Dev/LINA)까지다: {v}", tops="·".join(allowed or ["<영역>"]),
              v=v or t("(없음)")))
    return "/".join([top] + parts[1:])


def find_area(recs: dict, v: str):
    if v and v.upper() in recs and recs[v.upper()]["kind"] == "영역":
        return recs[v.upper()]
    path = norm_area(v)
    return next((r for r in recs.values() if r["kind"] == "영역" and r.get("title") == path
                 and r.get("state") != "dropped"), None)


def ensure_area(recs: dict, v: str, by: str) -> dict:
    """caller holds Lock(). The area record, created (owner = by) on first use."""
    r = find_area(recs, v)
    if r:
        return r
    rec = {"id": next_id(recs, "영역"), "kind": "영역", "by": by, "title": norm_area(v), "quote": "", "links": [],
           "notes": [], "state": "open", "created": now(), "progress_at": now(), "parent": ""}
    recs[rec["id"]] = ledger.append(rec, "add", by)
    return recs[rec["id"]]


def norm_project(v: str) -> str:
    v = (v or "").strip().strip("/")
    if not v:
        return ""
    desk = current().desk
    if desk:
        pre = str(desk).strip("/") + "/"
        if v.lstrip("/").startswith(pre):
            v = v.lstrip("/")[len(pre):]
    v = v.removeprefix("desk:")
    project_path = re.search(r"(?:^|/)(1_Projects/|4_Archive/\d{4}/)", v)
    if project_path:
        v = v[project_path.start(1):]
    if not re.match(r"(1_Projects|4_Archive/\d{4})/", v):
        v = "1_Projects/" + v
    return v


def split_projects(vals) -> list[str]:
    """--project values (repeatable, comma-separated) -> normalized, de-duplicated list in given order."""
    out = []
    for v in vals or []:
        for x in v.split(","):
            x = norm_project(x)
            if x and x not in out:
                out.append(x)
    return out


def norm_session(s: str, owner_id: str) -> str:
    from hermes_cli.work.people import agent
    s = s.strip().removeprefix("session:")
    if ":" in s:
        p, n = s.split(":", 1)
        return f"{agent(p)}:{n}"
    return f"{owner_id}:{s}"


def due_of(value, kind: str, board: bool = False) -> str:
    """--due -> '' (none or cleared) or a real YYYY-MM-DD; only goals and to-dos carry one, and a memo the owner hands
    an agent from the board's quick add (--via board)."""
    if value is None:
        return ""
    v = value.strip()
    if v and kind not in ("목표", "할 일") and not (board and kind == "메모"):
        die(t("--due는 목표·할 일에만 쓴다"))
    if v:
        try:
            ok = bool(re.fullmatch(r"\d{4}-\d{2}-\d{2}", v)) and datetime.date.fromisoformat(v)
        except ValueError:
            ok = False
        if not ok:
            die(t("--due는 YYYY-MM-DD다: {v} · work fields 마감일", v=v))
    return v


def due_hint(r: dict, texts) -> None:
    """One line at the end of add/edit/note output when the text names a date and the item has no due date."""
    if r.get("kind") in ("목표", "할 일") and not r.get("due") and any(date_word(x or "") for x in texts):
        print(t("날짜가 보인다. 마감일이면: work edit {id} --due YYYY-MM-DD", id=r["id"]))


def parse_day(s: str) -> int:
    s = s.strip()
    m = re.match(r"\+(\d+)([hd])$", s)
    if m:
        return now() + int(m.group(1)) * (3600 if m.group(2) == "h" else 86400)
    for fmt in ("%Y-%m-%d %H:%M", "%Y-%m-%d", "%m-%d"):
        try:
            tm = time.strptime(s, fmt)
            if fmt == "%m-%d":
                tm = time.strptime(f"{time.localtime(now()).tm_year}-{s}", "%Y-%m-%d")
            ts = int(time.mktime(tm))
            return ts + (86399 if fmt != "%Y-%m-%d %H:%M" else 0)
        except ValueError:
            continue
    die(t("날짜 형식: YYYY-MM-DD, MM-DD, +3d, +6h ({s})", s=s))


def mutate(i: str, op: str, fn, actor=None) -> dict:
    with Lock():
        recs, _ = ledger.read_all()
        r = dict(get(recs, i))
        if r.get("state") in ("done", "dropped") and op not in OPEN_OPS:
            die(t("{id}는 이미 {state} 상태다", id=r["id"], state=status(r["state"])))
        if r["kind"] in ("영역", "메모") and op in ("evidence", "wait"):
            die(t("{kinds}에는 증거·기다림을 붙이지 않는다. 목표나 할 일에 붙인다",
                  kinds=kind_word("영역") + "·" + kind_word("메모")))
        fn(r)
        ledger.append(r, op, actor)
    return r
