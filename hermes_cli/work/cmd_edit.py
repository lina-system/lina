"""``work edit``, ``work sprint`` and ``work archive``."""

from __future__ import annotations

import re
import time

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, die, now
from hermes_cli.work.items import due_hint, due_of, find_area, get, mutate, norm_session, split_projects
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.ledger import projects_of
from hermes_cli.work.links import refuse_reason
from hermes_cli.work.locale import kind, status, t
from hermes_cli.work.notify import tell_owner_agent
from hermes_cli.work.people import agent, name, owner, owner_or_agent
from hermes_cli.work.sources import basis_of, basis_profile, desk_file, new_basis
from hermes_cli.work.view import archive_due_projects, proj_name
from hermes_cli.work.writing import guard_text, missing_field

PARENT_KIND = {"목표": "영역", TODO: "목표", "메모": "영역"}
EDIT_FIELDS = ("title", "why", "done", "check", "session", "parent", "every", "owner", "project", "add_project",
               "remove_project", "stall", "decide", "due")


def _guards(r: dict, a) -> None:
    if a.title is not None and kind_now(r["kind"]) in ("목표", TODO):
        guard_text("목표 이름" if r["kind"] == "목표" else "할 일 제목", a.title)
    if a.done is not None and kind_now(r["kind"]) in ("목표", TODO):
        guard_text("완료 조건", a.done)
    if a.why is not None:
        if kind_now(r["kind"]) != TODO:
            die(t("--why는 할 일에만 쓴다"))
        if not a.why.strip():
            missing_field("왜", "--why")
        guard_text("왜", a.why)
        r["why"] = a.why.strip()
    if a.decide is not None:
        guard_text("결정 문장", a.decide)


def _plain(r: dict, a) -> None:
    for k in ("title", "done", "check", "every"):
        v = getattr(a, k)
        if v is None:
            continue
        if k == "check" and refuse_reason(v):
            die(t("검사 거절: {why}", why=refuse_reason(v)))
        r[k] = v
    if a.session is not None:
        r["session"] = norm_session(a.session, r["by"]) if a.session else ""
    if a.parent is not None:
        recs, _ = ledger.read_all()
        want = PARENT_KIND.get(kind_now(r["kind"]))
        pr = find_area(recs, a.parent) if want == "영역" else recs.get(a.parent.upper())
        if not want or not pr or pr["kind"] != want:
            die(t("{kind}의 상위는 {want}이어야 한다", kind=kind(r["kind"]), want=kind(want) if want else t("없음")))
        r["parent"] = pr["id"]
    if a.stall is not None:
        r["stall"] = a.stall


def _due_projects(r: dict, a) -> None:
    if a.due is not None:
        if kind_now(r["kind"]) not in ("목표", TODO):
            die(t("--due는 목표·할 일에만 쓴다"))
        due = due_of(a.due, r["kind"])
        if due:
            r["due"] = due
        else:
            r.pop("due", None)
    if r["kind"] == "목표" and (a.project is not None or a.add_project or a.remove_project):
        pjs = split_projects(a.project) if a.project is not None else projects_of(r)
        pjs += [x for x in split_projects(a.add_project) if x not in pjs]
        drop = split_projects(a.remove_project)
        missing = [x for x in drop if x not in pjs]
        if missing:
            die(t("{id}에 없는 프로젝트: {names}", id=r["id"], names=", ".join(missing)))
        r["projects"] = [x for x in pjs if x not in drop]
        r.pop("project", None)


def _owner_change(r: dict, a) -> None:
    if not a.owner:
        return
    new = owner_or_agent(a.owner)
    if new == owner():
        if kind_now(r["kind"]) != TODO:
            die(t("주인은 할 일만 맡는다"))
        if r["by"] != owner():
            r["from"] = r["by"]  # the agent that held it stays on as the one who asked
        r["session"] = ""
    else:
        r.pop("from", None)
    r["by"] = new


def _decide(r: dict, a, answered: list) -> None:
    if a.decide is None:
        return
    if r.get("decide_cid") and a.decide.strip() != r.get("decide"):
        # the owner answered the decision a reply asked for: the comment stays open for the agent's next reply
        cid = r.pop("decide_cid")
        r["comments"] = [dict(c, decided_at=now()) if c.get("cid") == cid else c for c in r.get("comments") or []]
        answered.append(cid)
    r["decide"] = a.decide.strip()
    if not r["decide"]:
        r.pop("decide_choices", None)
        r.pop("decide_recommend", None)


def _quote(r: dict, a, bases: list, profile: str) -> None:
    if a.basis:
        for q in a.basis:
            q = q.strip()
            if q and not any(x["text"] == q for x in bases):
                bases.append(new_basis(q, a.said, profile))
        r["basis"] = bases
    if a.quote is None:
        return
    q = a.quote.strip()
    if not q:
        die(t("--quote는 비울 수 없다. 주인이 한 말 그대로를 넣는다"))
    if not (a.source or "").strip():
        die(t("--quote를 바꿀 때는 --source \"<주인 말의 출처: 날짜·채널·메시지>\"가 필요하다"))
    old_q = r.get("quote", "")
    if q != old_q:
        r.setdefault("quote_history", []).append({"quote": old_q, "replaced_at": now(), "source": a.source.strip()})
    # --quote replaces/reverifies this entry; changed text stays in quote_history.
    b = [x for x in bases if x.get("text") not in (old_q, q)]
    r["quote"], r["quote_source"] = q, a.source.strip()
    r["basis"] = b + [new_basis(q, a.source, profile, a.said)]


def cmd_edit(a) -> None:
    answered: list[str] = []

    def f(r):
        profile = agent(a.as_) if a.as_ else r["by"]
        bases = basis_of(r)  # keep legacy basis provenance before owner/actor/writer change
        for b in bases:
            b.setdefault("profile", basis_profile(r, b))
        if bases:
            r["basis"] = bases
        _guards(r, a)
        _plain(r, a)
        _due_projects(r, a)
        _owner_change(r, a)
        _decide(r, a, answered)
        _quote(r, a, bases, profile)
        r["progress_at"] = now()
    only_quote = (a.quote is not None or a.basis) and all(getattr(a, k) is None for k in EDIT_FIELDS)
    r = mutate(a.id, "quote" if only_quote else "edit", f, a.as_)
    from hermes_cli.work.cmd_comments import reply_hint
    for cid in answered:
        tell_owner_agent(r, t("[대시보드] 주인이 {id} {cid} 결정에 답함 — {hint}", id=r["id"], cid=cid,
                              hint=reply_hint(r, cid)))
    print("ok")
    due_hint(r, (a.title, a.why))


# ---------------------------------------------------------------- sprint checklist
SPRINT_MARK = {"done": "✓", "doing": "◐", "todo": "○"}


def sprint_title(r: dict, recs: dict) -> str:
    parent = recs.get(r.get("parent"), {})
    title = parent.get("title", "") if parent.get("kind") == "목표" else r.get("title", "")
    match = re.search(r"(?:스프린트|[Ss]print)\s*([0-9]+[A-Za-z]?)", r.get("title") or "")
    return title + " · " + t("스프린트") + (" " + match.group(1) if match else "")


def sprint_card(r: dict, recs: dict) -> dict:
    items = (r.get("sprint") or {}).get("items") or []
    return {"title": sprint_title(r, recs), "done": sum(x.get("state") == "done" for x in items), "total": len(items)}


def sprint_text(value: str, label: str, limit: int, words: bool = False) -> None:
    if len(value) > limit or "\n" in value or "\r" in value or (words and not 1 <= len(value.split()) <= 5):
        die(t("{label}의 길이나 단어 수가 맞지 않는다", label=label))
    if re.search(r"[#/_`]", value) or re.search(r"(?<![A-Za-z0-9])[AGTM][0-9]+(?![A-Za-z0-9])", value):
        die(t("{label}에 금지된 문자나 항목 번호가 있다", label=label))


def sprint_print(r: dict) -> None:
    recs, _ = ledger.read_all()
    sprint = r.get("sprint") or {}
    if not sprint.get("items"):
        die(t("체크리스트 없음"), code=1)
    card = sprint_card(r, recs)
    print(f"{card['title']}  {card['done']}/{card['total']}")
    for item in sprint.get("items") or []:
        print(f"{SPRINT_MARK[item['state']]} {item['name']}")
        if item["state"] == "doing" and item.get("now"):
            print("   " + item["now"])
    print(t("주인이 할 것: {what}", what=sprint.get("owner") or t("없음")))


def _sprint_args(a) -> None:
    if a.action == "set":
        sprint_text(a.item, t("항목"), 30, True)
        if a.now is not None:
            if a.state != "doing":
                die(t("지금은 하는 중 항목에만 쓴다"))
            sprint_text(a.now, t("지금"), 40)
        return
    if (a.text is None) == (not a.none):
        die(t("할 일 문구 또는 --none 중 하나를 골라야 한다"))
    if a.text is not None:
        sprint_text(a.text, t("주인이 할 것"), 40)


def cmd_sprint(a) -> None:
    if a.action == "show":
        recs, _ = ledger.read_all()
        return sprint_print(get(recs, a.id))
    _sprint_args(a)

    def change(r):
        if kind_now(r.get("kind")) != TODO:
            die(t("체크리스트는 할 일(점검)에만 단다"))
        if a.action == "owner" and not (r.get("sprint") or {}).get("items"):
            die(t("체크리스트 항목을 먼저 넣는다"))
        sprint = r.setdefault("sprint", {"items": [], "owner": ""})
        if a.action == "owner":
            sprint["owner"] = a.text if a.text is not None else ""
            return
        items = sprint.setdefault("items", [])
        item = {"name": a.item, "state": a.state}
        if a.now is not None:
            item["now"] = a.now
        index = next((i for i, old in enumerate(items) if old.get("name") == a.item), len(items))
        if index == len(items):
            items.append(item)
        else:
            items[index] = item
        r["progress_at"] = now()
    sprint_print(mutate(a.id, "sprint", change, a.as_))
    return None


# ---------------------------------------------------------------- archive
def cmd_archive(a) -> None:
    """A closed goal whose Desk project folder can move to 4_Archive/YYYY/. The record never moves Desk files; it tells
    the owning agent what to move, and after the move `work archive <G> --moved` rewrites the tag."""
    recs, _ = ledger.read_all()
    r = get(recs, a.id)
    if r["kind"] != "목표" or not projects_of(r):
        die(t("Desk 프로젝트 폴더가 붙은 목표만 보관한다"))
    dst_of = {pj: f"4_Archive/{time.strftime('%Y', time.localtime(now()))}/{proj_name(pj)}" for pj in projects_of(r)}
    if a.moved:
        return _archive_moved(a, dst_of)
    due = archive_due_projects(r, recs)
    if not due:
        open_g = sorted({x["id"] for x in recs.values() if x["kind"] == "목표" and x.get("state") == "open"
                         and set(projects_of(x)) & set(projects_of(r))})
        die(t("아직 보관할 때가 아니다: 상태 {state}", state=status(r.get("state")) or r.get("state"))
            + (t(", 같은 프로젝트의 열린 목표 {ids}", ids=" ".join(open_g)) if open_g else ""))
    for pj in due:
        print(t("맡은 에이전트 {who}: Desk '{src}' → '{dst}' 로 옮기고 `work archive {id} --moved`", who=name(r["by"]),
                src=pj, dst=dst_of[pj], id=r["id"]))
    return None


def _archive_moved(a, dst_of: dict) -> None:
    moved = {pj: d for pj, d in dst_of.items() if desk_file(d) is not None and desk_file(d).exists()}
    if not moved:
        die(t("Desk에 {dst}가 없다. 옮긴 뒤 다시", dst=", ".join(dst_of.values())))
    with Lock():
        recs, _ = ledger.read_all()
        for x in list(recs.values()):
            if x["kind"] == "목표" and any(pj in moved for pj in projects_of(x)):
                nx = [moved.get(pj, pj) for pj in projects_of(x)]
                if isinstance(x.get("projects"), list):
                    ledger.append(x, "archive", a.as_ or x["by"], projects=nx)
                else:  # a legacy one-string record keeps its shape
                    ledger.append(x, "archive", a.as_ or x["by"], project=nx[0])
    print(t("태그를 {dst}로 바꿨다", dst=", ".join(moved.values())))
