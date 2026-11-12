"""An item's computed state (``computed`` in ``work json``): its check, its links, its session, its waits and its
to-dos, from the record and read-only sources only. ``state.recompute`` runs it the moment an item is written."""

from __future__ import annotations

import re
import time

from hermes_cli.work.common import ago, now
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.links import (
    card_key, gh_repo, kanban_ended, kanban_read, kanban_state_word, link_label, refuse_reason, run_check,
)
from hermes_cli.work.locale import t
from hermes_cli.work.people import agent_name, owner
from hermes_cli.work.sources import (
    gh_fetch, iso_ts, link_path_exists, session_state, todoist_state, verify_evidence,
)

ALERT = {"stalled", "check_left", "bad"}
WORK_LINKS = ("gh", "ms", "session", "kanban", "omo")
OMO_NEXT_LIMIT = 30 * 60  # a to-do added with --omo next and still no omo:OT-… link this long reads as "no run yet"
RUN_ACTIVE = ("in_progress", "waiting", "planned")


def item_name(r: dict) -> str:
    from hermes_cli.work.sources import basis_of
    b = basis_of(r)
    return r.get("title") or (b[-1]["text"][:40] if b else "")


def item_waits(w: dict, ctx: dict):
    """The open item a wait names by id (work wait --on-item), else None."""
    target = (ctx.get("recs") or {}).get(w.get("item")) if w.get("item") else None
    return target if target and target.get("state") == "open" else None


def omo_next_late(r: dict) -> bool:
    at = r.get("omo_next_at")
    return bool(at) and r.get("state") == "open" and now() - int(at) >= OMO_NEXT_LIMIT


def _evidence_result(r: dict, ctx: dict, chk: str, at: int) -> dict:
    good, unverified = [], []
    for e in r.get("evidence") or []:
        (good if verify_evidence(e.get("ref", ""), ctx) is True else unverified).append(e.get("ref"))
    if good:
        shown = ", ".join(link_label(x) if ":" in x else x for x in good)[:300]
        return {"rc": 0, "out": t("증거 확인: {refs}", refs=shown), "secs": 0, "at": at}
    if unverified:
        return {"rc": 3, "out": t("증거가 붙었지만 기계로 확인 못 함 — 에이전트 직접 확인(work confirm --seen) 또는 "
                                  "주인의 말"), "secs": 0, "at": at, "evidence_wait": True}
    return {"rc": 3, "out": t("증거 없음: {what}", what=chk[9:]), "secs": 0, "at": at}


def check_result(r: dict, ctx: dict, run_checks: bool, at: int):
    chk = r.get("check") or ""
    if chk.startswith("evidence:"):
        return _evidence_result(r, ctx, chk, at)
    if not chk:
        return None
    why = refuse_reason(chk)
    if why:
        return {"rc": 126, "out": t("검사 거절: {why}", why=why), "secs": 0, "at": at}
    if run_checks:
        return run_check(chk)
    return (ctx.get("prev") or {}).get(r["id"], {}).get("check")


def _periodic(r: dict, c: dict, res, passed: bool, at: int) -> dict:
    c["status"] = "ok" if passed else ("bad" if res else "unknown")
    c["line"] = t("정상") if passed else (t("이상: {out}", out=(res.get("out") or "")[:80]) if res else t("검사 없음"))
    if res and not passed and res.get("rc") in (124, 126, 127):
        c["line"] = t("검사 못 함: {out}", out=(res.get("out") or "")[-80:])
    if not passed:
        c["reasons"].append(t("검사 실패"))
    w = r.get("wait") or {}
    if w.get("hold"):
        c.update(status="waiting", reasons=[], line=t("보류: {on}", on=w.get("on") or ""))
    elif w.get("next") and w["next"] > at:
        c.update(status="waiting", reasons=[],
                 line=t("기다림: {on} ({day}까지) · 마지막 검사 {line}", on=w.get("on", ""),
                        day=time.strftime("%m-%d", time.localtime(w["next"])), line=c["line"]))
    return c


def _owner_item(r: dict, c: dict, ctx: dict) -> dict:
    # The owner is never chased: no stall, no alarm. The requesting agent watches it.
    w = r.get("wait") or {}
    pre = item_waits(w, ctx)
    if pre or w.get("hold") or w.get("on"):
        c.update(status="waiting", line=t("진행 대기: {what}", what=item_name(pre) if pre else w.get("on") or ""))
    else:
        c.update(status="open", line=t("요청: {who}", who=agent_name(r.get("from"), "")))
    return c


class Scan:
    def __init__(self):
        self.active, self.unattended, self.stuck, self.finished, self.total = False, [], [], 0, 0
        # a card is the to-do's runner: one it waits on, one that ended, and one nobody can read (#249)
        self.card_wait: list[str] = []
        self.card_closed: list[str] = []
        self.card_unread: list[str] = []


def _gh(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    spec, n = link[3:].split("#")
    o, repo = gh_repo(spec)
    d = gh_fetch(f"repos/{o}/{repo}/issues/{n}", ctx["gh"])
    s.total += 1
    if not d:
        td["state"] = t("모름(GitHub 못 읽음)")
        return
    if d.get("updated_at"):
        c["activity"] = max(c["activity"], iso_ts(d["updated_at"]))
    if d.get("state") == "closed":
        s.finished += 1
        td["state"] = t("합쳐짐") if d.get("merged") else t("닫힘")
    elif d.get("is_pr"):
        s.active = td["active"] = True
        td["state"] = t("PR 열림")
    else:
        td["state"] = t("열림")


def _ms(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    spec, n = link[3:].rsplit("/", 1)
    o, repo = gh_repo(spec)
    d = gh_fetch(f"repos/{o}/{repo}/milestones/{n}", ctx["gh"])
    if not d:
        td["state"] = t("모름")
        return
    closed, opened = d.get("closed_issues") or 0, d.get("open_issues") or 0
    td["state"] = t("이슈 {closed}/{total} 닫힘", closed=closed, total=opened + closed)
    s.total += 1
    if opened == 0:
        s.finished += 1
    else:
        td["active"] = True
    if d.get("updated_at"):
        c["activity"] = max(c["activity"], iso_ts(d["updated_at"]))


def _session(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    v, e = link[8:], None
    if ":" in v and not re.match(r"\d{8}-\d{6}-", v):
        p, n = v.split(":", 1)
        e = ctx["sess"].get((p, n))
    else:
        x = ctx["sess_id"].get(v)
        if x:
            e = ctx["sess"].get((x.get("profile"), x.get("name")))
    code, lab, act = session_state(e)
    td["state"] = lab
    c["activity"] = max(c["activity"], act)
    if code == "active":
        s.active = td["active"] = True


def _kanban(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    """A card is the to-do's runner, so its own state is the to-do's (#249): a live card is the work, a blocked card
    (or one archived before it finished) is waited on, a card that ended asks for the confirmation, and a card nobody
    can read is 모름 — never 끝남."""
    raw = kanban_read(link)
    td["state"] = kanban_state_word(raw)
    s.total += 1
    if raw == "running":
        s.active = td["active"] = True  # a live card is work nobody else has to take
    elif raw == "blocked" or (raw == "archived" and not kanban_ended(link)):
        s.card_wait.append(td["label"])
    elif kanban_ended(link):
        s.finished += 1
        s.card_closed.append(td["label"])
    elif not raw:
        s.card_unread.append(td["label"])


def _omo(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    """A worker run on the to-do (run_todo): live while it runs; its end never closes the to-do."""
    status, reason = ctx.get("runs", {}).get(link[4:], (None, None))
    if status is None:
        td["state"] = t("작업 기록 없음")
        return
    s.total += 1
    if status in RUN_ACTIVE:
        s.active = td["active"] = True
        td["state"] = t("작업 {code}", code=status)
    elif status == "stopped":
        s.stuck.append(td["label"])
        td["state"] = t("작업 멈춤: {reason}", reason=reason or "")
    elif status == "done":
        s.finished += 1
        td["state"] = t("작업 끝남, 이 할 일의 검사 남음")
    else:
        td["state"] = t("작업 {code}", code=status)


def _todoist(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    td["state"] = todoist_state(link[8:])
    if td["state"] == t("안 끝남"):
        s.active = td["active"] = True


def _path(link: str, td: dict, c: dict, ctx: dict, s: Scan) -> None:
    there = link_path_exists(link)
    td["state"] = t("모름") if there is None else (t("있음") if there else t("없음"))


def card_decides(r: dict, ctx: dict) -> list[str]:
    """#249: the kanban links whose end passes this to-do's check, or []. Only a to-do with no check of its own (empty
    or evidence:), whose every card ended, and whose cards (compared as cards, ``card_key``) carry no other open or
    done to-do."""
    if kind_now(r["kind"]) != TODO or ((r.get("check") or "") and not r["check"].startswith("evidence:")):
        return []
    cards = [x for x in r.get("links") or [] if x.startswith("kanban:")]
    if not cards or not all(kanban_ended(x) for x in cards):
        return []
    keys = {card_key(x) for x in cards}
    for x in (ctx.get("recs") or {}).values():
        if x["id"] != r["id"] and x.get("state") in ("open", "done") and keys & {
                card_key(y) for y in x.get("links") or [] if y.startswith("kanban:")}:
            return []
    return cards


SCANNERS = {"gh": _gh, "ms": _ms, "session": _session, "kanban": _kanban, "omo": _omo, "todoist": _todoist,
            "desk": _path, "file": _path}


def scan_links(r: dict, c: dict, ctx: dict) -> Scan:
    """A stopped run that a later run of the same to-do redid (one that ended done, or is live) is no longer blocked
    work and counts toward nothing: runs are linked in the order they were handed out (lina#607)."""
    s = Scan()
    for link in r.get("links") or []:
        td = {"link": link, "label": link_label(link), "state": "", "active": False}
        fn = SCANNERS.get(link.split(":", 1)[0])
        finished = s.finished
        if fn:
            fn(link, td, c, ctx, s)
        if link.startswith("omo:") and (td["active"] or s.finished > finished):
            s.total -= len(s.stuck)
            s.stuck.clear()
        c["todos"].append(td)
    return s


def _manager(r: dict, c: dict, ctx: dict, s: Scan):
    if not r.get("session"):
        return None
    p, n = r["session"].split(":", 1) if ":" in r["session"] else (r["by"], r["session"])
    code, lab, act = session_state(ctx["sess"].get((p, n)))
    c["manager"] = lab
    c["activity"] = max(c["activity"], act)
    if code == "active":
        s.active = True
    return code


def _goal_kids(r: dict, c: dict, ctx: dict, s: Scan):
    kids = [x for x in ctx["recs"].values() if x.get("parent") == r["id"] and kind_now(x["kind"]) == TODO
            and x.get("state") != "dropped"]
    kd = [x for x in kids if x.get("state") == "done" or ctx["prev"].get(x["id"], {}).get("pass")]
    c["kids"] = [len(kd), len(kids)]
    if not kids:
        return None
    kst = {x["id"]: ctx["prev"].get(x["id"], {}) for x in kids if x not in kd}
    bad = [i for i, v in kst.items() if v.get("status") in ALERT]
    live = [i for i, v in kst.items() if v.get("status") in ("working", "waiting", "evidence_wait")]
    for v in kst.values():
        c["activity"] = max(c["activity"], int(v.get("activity") or 0))
    if len(kd) == len(kids) and not s.active:
        return c.update(status="check_left", line=t("할 일 {n}개 다 끝났는데 목표 검사가 안 통과", n=len(kids)),
                        reasons=[t("검사 남음")]) or c
    if bad:
        names = [ctx["recs"][i].get("title") or i for i in bad]
        why = [(kst.get(i, {}).get("reasons") or [""])[0] for i in bad]
        rs = "; ".join(f"{n} — {w}" if w else n for n, w in zip(names, why))
        return c.update(status="stalled", line=t("할 일 {done}/{total} 끝남, 멈춘 할 일: {rs}", done=len(kd),
                                                  total=len(kids), rs=rs),
                        reasons=[rs], kid_stall=True, bad_ids=bad) or c
    if live or s.active:
        return c.update(status="working", line=t("할 일 {done}/{total} 끝남", done=len(kd), total=len(kids))) or c
    return None


def _warnings(s: Scan, c: dict, ms_code) -> list[str]:
    warn = []
    if s.unattended:
        warn.append(t("아무도 안 붙은 이슈: {items}", items=", ".join(s.unattended)))
    if s.stuck:
        warn.append(t("막힌 작업: {items}", items=", ".join(s.stuck)))
    if ms_code == "dead":
        warn.append(t("맡은 세션 {state}", state=c.get("manager", t("끊김"))))
    return warn


def _stall_hours(r: dict) -> float:
    if r.get("stall"):
        return float(r["stall"])
    linked = r.get("session") or any(x.split(":")[0] in WORK_LINKS for x in r.get("links") or [])
    return 2.0 if linked else 24.0


def _reasons(r: dict, c: dict, s: Scan, ms_code, warn: list[str], at: int) -> list[str]:
    reasons = []
    if s.unattended and ms_code != "active" and s.active:
        reasons.append(t("아무도 안 붙은 이슈: {items} (맡은 세션도 안 돎)", items=", ".join(s.unattended)))
    if not s.active:
        reasons += warn
        if ms_code == "ended":
            reasons.append(t("맡은 세션이 끝났는데 검사가 안 통과"))
        idle = at - c["activity"]
        if idle >= _stall_hours(r) * 3600:
            reasons.append(t("아무도 안 붙음") if not (r.get("links") or r.get("session"))
                           else t("진척 없음 {ago}", ago=ago(c["activity"])))
    if omo_next_late(r):
        reasons.insert(0, t("실행 이력 없음"))
    return reasons


def wait_live(w: dict, ctx: dict, at: int) -> bool:
    """A wait in force now: on another open item, or until a date still ahead."""
    return bool(item_waits(w, ctx) or (w.get("next") and w["next"] > at))


def wait_after_evidence(r: dict, w: dict, waiting: bool) -> bool:
    """A live wait counts after ``evidence_wait`` when it was set after the newest evidence: the agent read the
    evidence, found it short of the done condition and said what it now waits for, so the confirm nudge would only cost
    a turn. Only a strictly newer wait counts, so evidence attached after the wait brings ``evidence_wait`` back."""
    if not waiting:
        return False
    newest = max((int(e.get("ts") or 0) for e in r.get("evidence") or []), default=0)
    return int(w.get("since") or 0) > newest


def _waiting(r: dict, c: dict, ctx: dict, warn: list[str], at: int):
    w = r.get("wait") or {}
    if not wait_live(w, ctx, at):
        return None
    on_item = item_waits(w, ctx)
    if on_item:
        return c.update(status="waiting", line=t("기다림: 주인 할 일 {name}", name=item_name(on_item)),
                        reasons=warn) or c
    return c.update(status="waiting", line=t("기다림: {on} ({day}까지)", on=w.get("on", ""),
                                              day=time.strftime("%m-%d", time.localtime(w["next"]))),
                    reasons=warn) or c


def _cards(r: dict, c: dict, s: Scan):
    """The card's own state is the to-do's (#249): a card it waits on outranks everything and raises no alert; then a
    stopped run; then a card that ended, which leaves the check or asks for a confirmation when the to-do has no
    command check of its own; then a card nobody can read, which is 모름. A live card outranks all four."""
    if s.active:
        return None
    if s.card_wait:
        return c.update(status="waiting", line=t("카드 기다림: {label}", label=s.card_wait[0]), reasons=[]) or c
    if s.stuck:
        return None  # a stopped run is what the to-do needs to hear about, not its card
    chk = r.get("check") or ""
    if s.card_closed and chk and not chk.startswith("evidence:"):
        return c.update(status="check_left", line=t("{label} 끝남, 검사가 안 통과", label=s.card_closed[0]),
                        reasons=[t("검사 남음")]) or c
    if s.card_closed:
        return c.update(status="evidence_wait", line=t("{label} 끝남 — 확인 필요", label=s.card_closed[0]),
                        reasons=[]) or c
    if s.card_unread:
        return c.update(status="unknown", line=t("카드 상태 모름 — {label}", label=s.card_unread[0]),
                        reasons=[]) or c
    return None


def _decide(r: dict, c: dict, ctx: dict, s: Scan, ms_code, res, at: int) -> dict:
    if s.active:
        c["activity"] = at  # a running session/job/PR is alive; stall = nothing alive and no progress
    warn = _warnings(s, c, ms_code)
    c["warn"] = warn
    if r["kind"] == "목표":
        done = _goal_kids(r, c, ctx, s)
        if done is not None:
            return done
    w = r.get("wait") or {}
    if res and res.get("evidence_wait") and not wait_after_evidence(r, w, wait_live(w, ctx, at)):
        return c.update(status="evidence_wait", line=t("증거 붙음 — 확인 필요"), reasons=[]) or c
    waited = _waiting(r, c, ctx, warn, at)
    if waited is not None:
        return waited
    cards = _cards(r, c, s)
    if cards is not None:
        return cards
    if res and res.get("rc") not in (0, None):
        head = t("검사 못 함: ") if res.get("rc") in (124, 126, 127) else t("검사 미통과: ")
        c["check_line"] = head + (res.get("out") or "")[-120:]
    if s.total and s.finished == s.total and not s.active:
        return c.update(status="check_left", line=t("할 일은 다 닫혔는데 검사가 안 통과"), reasons=[t("검사 남음")]) or c
    reasons = _reasons(r, c, s, ms_code, warn, at)
    if reasons:
        c.update(status="stalled", line=reasons[0], reasons=reasons)
    elif s.active:
        act = [x for x in c["todos"] if x["active"]]
        c.update(status="working", line=(f"{act[0]['label']} {act[0]['state']}" if act
                                         else t("맡은 세션 {state}", state=c.get("manager", t("도는 중")))))
    else:
        c.update(status="working", line=t("최근 진척 {ago} 전", ago=ago(c["activity"])))
    return c


def _closed(r: dict, c: dict):
    if r.get("state") == "done":
        return c.update(status="done", line=t("끝남({by}) {ago} 전", by=r.get("done_by", t("검사")),
                                               ago=ago(r.get("done_at")))) or c
    if r.get("state") == "dropped":
        return c.update(status="dropped", line=t("주인이 내림")) or c
    return None


def evaluate(r: dict, ctx: dict, run_checks: bool = True) -> dict:
    """-> computed dict (no record writes)."""
    at = now()
    c = {"id": r["id"], "status": "unknown", "reasons": [], "todos": [], "line": "",
         "activity": int(r.get("progress_at") or r.get("created") or 0)}
    if _closed(r, c) is not None:
        return c
    res = check_result(r, ctx, run_checks, at)
    # #249: the cards that carry only this to-do ended, and the to-do has no check of its own: that is the check.
    done_cards = [] if (res and res.get("rc") == 0) else card_decides(r, ctx)
    if done_cards:
        res = {"rc": 0, "out": t("카드 끝남: {cards}", cards=", ".join(link_label(x) for x in done_cards)),
               "secs": 0, "at": at}
        c["done_cards"] = done_cards
    c["check"] = res
    passed = bool(res and res.get("rc") == 0)
    if r["kind"] == "영역":
        return c.update(status="open", line="") or c
    if r["kind"] == "메모":
        if at - c["activity"] >= 24 * 3600:
            return c.update(status="stalled", line=t("메모 정리 안 됨 {ago}", ago=ago(c["activity"])),
                            reasons=[t("메모를 목표로 올리거나 주인의 말로 버려야 한다")]) or c
        return c.update(status="memo", line=t("정리 전")) or c
    if r["kind"] == "목표" and r.get("every"):
        return _periodic(r, c, res, passed, at)
    if passed:
        return c.update(status="done", line=t("검사 통과"), **{"pass": True}) or c
    if r.get("by") == owner():
        return _owner_item(r, c, ctx)
    s = scan_links(r, c, ctx)
    ms_code = _manager(r, c, ctx, s)
    w = r.get("wait") or {}
    if w.get("hold"):
        return c.update(status="waiting", line=t("보류: {on}", on=w.get("on") or ""), reasons=[]) or c
    return _decide(r, c, ctx, s, ms_code, res, at)
