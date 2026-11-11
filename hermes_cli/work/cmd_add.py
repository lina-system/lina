"""``work add 영역|목표|할일|메모``: an area, a goal (with the owner's words and a check), a to-do (with its runner)
or a memo; the owner may hold to-dos of their own."""

from __future__ import annotations

import argparse
import re
from contextlib import nullcontext

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, die, now
from hermes_cli.work.items import (
    KIND_IN, due_hint, due_of, ensure_area, find_area, get, next_id, norm_session, split_projects,
)
from hermes_cli.work.kinds import TODO
from hermes_cli.work.links import OMO_ID, norm_link, refuse_reason
from hermes_cli.work.locale import t
from hermes_cli.work.notify import tell
from hermes_cli.work.people import agent, owner, owner_or_agent
from hermes_cli.work.sources import new_basis
from hermes_cli.work.writing import guard_text, missing_field


def _links(a, by: str):
    links = [norm_link(link, by) for link in a.link or []]
    omo = (a.omo or "").strip()
    if omo and omo != "next":
        if not re.fullmatch(OMO_ID, omo.removeprefix("omo:")):
            die(t("--omo 는 작업 id(예: OT-ABC1031071257) 또는 next(곧 작업이 붙음)여야 한다: {v}", v=omo))
        links = list(dict.fromkeys(links + [norm_link("omo:" + omo.removeprefix("omo:"), by)]))
    return links, omo == "next"


def _check_args(a, kind: str, by: str) -> bool:
    if not kind:
        die(t("종류는 영역|목표|할일|메모 (이슈는 work link로 붙인다)"))
    if by == owner() and kind != TODO:
        die(t("주인은 할 일만 맡는다(work add 할 일 --by {owner} --from <요청한 에이전트>)", owner=owner()))
    board = bool(a.via)
    if board and (kind != "메모" or by == owner()):
        die(t("--via board는 에이전트에게 맡기는 메모에만 쓴다"))
    if a.check:
        why = refuse_reason(a.check)
        if why:
            die(t("검사 거절: {why}", why=why))
    return board


def _area(a, recs: dict, by: str) -> None:
    if not (a.title or a.area):
        die(t("영역에는 --title \"<영역 경로>\" 가 필요하다(예: Dev/빌드 기기)"))
    if find_area(recs, a.title or a.area):
        die(t("이미 있는 영역: {id}", id=find_area(recs, a.title or a.area)["id"]))
    print(ensure_area(recs, a.title or a.area, by)["id"])


def _memo(a, recs: dict, by: str, basis: list) -> tuple[str, None]:
    if not basis:
        die(t("메모에는 --basis \"<주인이 한 말>\" 이 필요하다"))
    return (ensure_area(recs, a.area, by)["id"] if a.area else ""), None


def _goal(a, recs: dict, by: str, basis: list):
    if not basis:
        die(t("목표에는 --basis \"<주인이 한 말>\" 이 필요하다(여러 번 가능, --said로 시각·채널. 내 약속이면 --promise와 "
              "약속 문장)"))
    if not a.check:
        die(t("목표에는 --check \"<읽기 전용 검사 한 줄 | evidence:무엇>\" 이 필요하다"))
    if not (a.done or a.every):
        die(t("목표에는 --done \"<끝 조건>\" 이 필요하다(주기 목표는 --every 1d 등)"))
    if not a.area:
        die(t("목표에는 --area <영역> 이 필요하다"))
    guard_text("목표 이름", a.title)
    guard_text("완료 조건", a.done)
    parent = ensure_area(recs, a.area, by)["id"]
    memo = None
    if a.from_:
        memo = get(recs, a.from_)
        if memo["kind"] != "메모" or memo.get("state") != "open":
            die(t("{id}는 열린 메모가 아니다", id=a.from_))
    return parent, memo


def _todo_texts(a) -> None:
    if not (a.title or "").strip():
        missing_field("할 일 제목", "--title")
    if not (a.why or "").strip():
        missing_field("왜", "--why")
    guard_text("할 일 제목", a.title)
    guard_text("왜", a.why)
    guard_text("완료 조건", a.done)


def _owner_todo(a, recs: dict):
    for flag, value in (("--session", a.session), ("--omo", a.omo), ("--every", a.every), ("--stall", a.stall)):
        if value:
            die(t("주인 할 일에는 {flag}를 받지 않는다. 요청한 에이전트는 --from <에이전트>", flag=flag))
    if not a.from_:
        die(t("주인 할 일에는 --from <요청한 에이전트> 이 필요하다"))
    if not a.done:
        die(t("할 일에는 --done \"<끝 조건>\" 이 필요하다"))
    g = get(recs, a.goal) if a.goal else None
    if g and g["kind"] != "목표":
        die(t("--goal은 목표 id다"))
    _todo_texts(a)
    return (g["id"] if g else ""), None


def _agent_todo(a, recs: dict):
    if a.from_:
        die(t("할 일의 --from은 주인 할 일에만 쓴다(요청한 에이전트)"))
    if not (a.done and a.check):
        die(t("할 일에는 --done \"<끝 조건>\" 과 --check \"<검사>\" 가 필요하다"))
    if not (a.session or a.omo or any(str(link).strip().startswith("kanban:") for link in a.link or [])):
        die(t("할 일에는 --session <맡은 주제 세션 이름>, --omo <맡은 작업 id, 예: OT-ABC1031071257 | next(곧 작업이 "
              "붙음)> 또는 --link kanban:<작업 카드 id> 가 필요하다"))
    g = get(recs, a.goal) if a.goal else None
    if not g or g["kind"] != "목표":
        die(t("할 일에는 --goal <목표 id> 가 필요하다"))
    _todo_texts(a)
    return g["id"], None


def _parent(a, kind: str, recs: dict, by: str, basis: list):
    if kind in ("목표", "메모") and not (a.title or "").strip():
        die(t("--title \"<정규화한 제목>\" 이 필요하다: 결과가 보이는 짧은 명사구, 25자 안팎. 주인의 말은 --basis로"))
    if kind == "메모":
        return _memo(a, recs, by, basis)
    if kind == "목표":
        return _goal(a, recs, by, basis)
    return _owner_todo(a, recs) if by == owner() else _agent_todo(a, recs)


def _record(a, kind: str, recs: dict, by: str, basis: list, parent: str, links: list, due: str) -> dict:
    rec = {"id": next_id(recs, kind), "kind": kind, "by": by, "title": (a.title or "").strip(), "basis": basis,
           "promise": bool(a.promise), "done": a.done or "", "check": a.check or "", "decide": "",
           "parent": parent, "projects": split_projects(a.project) if kind == "목표" else [],
           "session": norm_session(a.session, by) if a.session else "",
           "links": links, "every": a.every or "", "stall": a.stall, "notes": [], "evidence": [], "wait": {},
           "state": "open", "created": now(), "progress_at": now()}
    if kind == TODO:
        rec["why"] = a.why.strip()
    if by == owner():
        rec["from"] = agent(a.from_)
    if due:
        rec["due"] = due
    return rec


def todo_args(*, by: str, goal: str, title: str, why: str, done: str, check: str, link: str) -> argparse.Namespace:
    """The arguments of ``work add 할 일 --by <by> --goal <goal> --title … --why … --done … --check … --link <link>``."""
    return argparse.Namespace(
        kind="할 일", by=by, goal=goal, title=title, why=why, done=done, check=check, link=[link], area=None,
        project=None, from_=None, due=None, basis=None, parent=None, session=None, omo=None, promise=False, said=None,
        every=None, stall=None, note=None, via=None)


def add(a, *, write: bool = True) -> dict | None:
    """``work add`` without the output: refuses as the command does, then (``write``) appends the item and returns it.
    ``write=False`` runs every check against the record read-only and writes nothing."""
    kind = KIND_IN.get((a.kind or "").strip())
    by = owner_or_agent(a.by)
    board = _check_args(a, kind, by)
    due = due_of(a.due, kind, board)
    links, omo_next = _links(a, by)
    with Lock() if write else nullcontext():
        recs, _ = ledger.read_all()
        if kind == "영역":
            _area(a, recs, by)
            return None
        basis = [new_basis(q.strip(), a.said, by) for q in (a.basis or []) if q.strip()]
        parent, memo = _parent(a, kind, recs, by, basis)
        if a.why and kind != TODO:
            die(t("--why는 할 일에만 쓴다"))
        guard_text("진행 메모", a.note)
        rec = _record(a, kind, recs, by, basis, parent, links, due)
        if not write:
            return rec
        if kind == TODO and omo_next and not any(link.startswith("omo:") for link in links):
            rec["omo_next_at"] = now()  # waits for `work link <id> omo:OT-…` (lina work run submit links it)
        if a.note:
            rec["notes"].append({"ts": now(), "by": rec.get("from", by), "text": a.note})
        if board:
            rec["from"], rec["via"] = owner(), a.via
        ledger.append(rec, "add", by)
        if memo:
            ledger.append(memo, "done", by, state="done", done_at=now(), done_by=t("목표 {id}로 올림", id=rec["id"]),
                          done_evidence=rec["id"])
    if board:
        tell(by, t("[대시보드] 주인이 맡김 {id}: {title}", id=rec["id"], title=rec["title"])
             + (t(" (마감일 {due})", due=due) if due else ""))
    return rec


def cmd_add(a) -> None:
    rec = add(a)
    if rec is None:
        return
    print(rec["id"])
    due_hint(rec, (a.title, a.why, a.note))
