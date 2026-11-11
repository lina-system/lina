"""What the board and ``list``/``show`` print for an item: the goal row (health dot, stage, last news), the last
computed state, the area -> goal -> to-do tree."""

from __future__ import annotations

import time

from hermes_cli.work.common import ago, now
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.ledger import projects_of
from hermes_cli.work.locale import STAGES, status, t
from hermes_cli.work.sources import desk_file, first_said

STALE_RESULT = 45 * 60  # a check result older than this reads as unknown
NEWS_EVERY = 6 * 3600   # a goal's owner leaves a `work note` at least this often (dotted at 1x, grey at 2x)
DEFINING, DOING, CHECK_LEFT, ENDED = STAGES
NOT_NEWS = ("Desk 1_Projects 폴더에서 등록", "주인의 말", "근거", "다시 엶:", "제목", "reopened:")


def _stage(r: dict, c: dict, kids: list[dict]) -> str:
    if c.get("status") == "check_left" or (kids and all(k.get("state") == "done" for k in kids)):
        return CHECK_LEFT
    if r.get("every") or kids or r.get("session") or any(
            link.split(":")[0] in ("gh", "ms", "dv", "session") for link in r.get("links") or []):
        return DOING
    return DEFINING  # method and scope not written down yet: no to-do, no runner


def _is_news(n: dict) -> bool:
    tx = n.get("text") or ""
    return bool(tx) and not tx.startswith(NOT_NEWS)


def _news(r: dict, kids: list[dict]):
    notes = [n for n in (r.get("notes") or []) if _is_news(n)]
    for k in kids:
        notes += [dict(n, kid=k.get("title")) for n in (k.get("notes") or []) if _is_news(n)]
    last = max(notes, key=lambda n: n.get("ts") or 0) if notes else None
    return (last or {}).get("text", ""), int((last or {}).get("ts") or 0)


def _wait_health(r: dict, kids: list[dict], at: int):
    open_kids = [k for k in kids if k.get("state") != "done"]
    own_wait = r.get("wait") or {}
    waits = [k.get("wait") or {} for k in open_kids]
    all_waiting = bool(waits) and all(w.get("hold") or w.get("next") or w.get("item") for w in waits)
    selected = [own_wait] if own_wait.get("hold") or own_wait.get("next") else (waits if all_waiting else [])
    if not selected:
        return None
    overdue = [w for w in selected if w.get("next") and w["next"] <= at]
    if overdue:
        return "yellow", t("기다림 기한 지남: {on}", on=min(overdue, key=lambda w: w["next"]).get("on") or "")
    if all(w.get("hold") for w in selected):
        return "hold", t("보류: {on}", on=selected[0].get("on") or "")
    dated = [w for w in selected if w.get("next")]
    if dated:
        soonest = min(dated, key=lambda w: w["next"])
        return "wait", t("기다림: {on} ({day}까지)", on=soonest.get("on") or "",
                         day=time.strftime("%m-%d", time.localtime(soonest["next"])))
    return "wait", t("기다림: 주인 할 일")


def _health(r: dict, c: dict, stage: str, kids: list[dict], idle: int, at: int):
    waited = _wait_health(r, kids, at)
    if waited:
        return waited
    st = c.get("status")
    if stage == DEFINING and at - first_said(r) > 7 * 86400:
        return "tidy", t("정리 대상")
    if st in ("stalled", "bad"):
        rs = [x for x in (c.get("reasons") or []) if x and x != t("검사 실패")]
        return "red", (rs or [c.get("line") or t("검사 실패")])[0]
    if stage == DEFINING and at - first_said(r) > 86400:
        return "yellow", t("{ago}째 방법·범위 안 정함", ago=ago(first_said(r)))
    if st in ("check_left", "evidence_wait") or (st == "working" and idle >= 3600 and not c.get("active_now")):
        return "yellow", (t("할 일은 끝, 검사 남음") if stage == CHECK_LEFT
                          else t("{n}시간째 진척 없음", n=max(1, idle // 3600)))
    if st == "unknown":
        return "grey", c.get("line") or t("검사 결과 없음")
    return "green", ""


def row_view(r: dict, c: dict, recs: dict) -> dict:
    """The goal row: health dot · stage · last news. Computed from checks and the record only."""
    at = now()
    if r.get("state") == "done":
        return {"stage": ENDED, "health": "green", "news": "", "news_at": int(r.get("done_at") or 0)}
    kids = [x for x in recs.values() if x.get("parent") == r["id"] and kind_now(x["kind"]) == TODO
            and x.get("state") != "dropped"]
    stage = _stage(r, c, kids)
    news, news_at = _news(r, kids)
    note, note_at = news[:240], news_at  # the owner's own last note, kept even when the checker fills news
    activity_at = int(c.get("activity") or 0)
    idle = at - int(activity_at or r.get("progress_at") or r.get("created") or 0)
    health, reason = _health(r, c, stage, kids, idle, at)
    quiet = at - max(news_at, activity_at, int(r.get("created") or 0))
    dotted = False
    if health == "green" and quiet > 2 * NEWS_EVERY:
        health, reason = "grey", t("소식 없음 {ago}", ago=ago(max(news_at, int(r.get("created") or 0))))
    elif health == "green" and quiet > NEWS_EVERY:
        dotted = True
    if health in ("yellow", "red") and reason and not (news and news_at >= at - 3600):
        news, news_at = reason, int(c.get("checked") or at)  # the checker fills the reason when the agent left none
    return {"stage": stage, "health": health, "dotted": dotted, "news": news[:60], "news_at": news_at,
            "reason": reason, "note": note, "note_at": note_at, "activity_at": activity_at}


def computed(r: dict, st: dict) -> dict:
    if r["kind"] == "영역":
        return {"status": "open" if r.get("state") == "open" else r.get("state"), "line": "", "reasons": []}
    c = (st.get("items") or {}).get(r["id"])
    if r.get("state") == "done":
        return {"status": "done", "line": t("끝남({by})", by=r.get("done_by", t("검사"))), "reasons": []}
    if r.get("state") == "dropped":
        return {"status": "dropped", "line": t("주인이 내림"), "reasons": []}
    if not c:
        return {"status": "unknown", "line": t("아직 검사 안 함"), "reasons": [], "stale": True}
    c = dict(c)
    if now() - int(c.get("checked") or 0) > STALE_RESULT:
        c["stale"] = True
        c["line"] = t("마지막 검사 {ago} 전 ({state}) — 검사기 확인 필요", ago=ago(c.get("checked")),
                      state=status(c["status"]))
        c["status_before"] = c["status"]
        c["status"] = "unknown"
    return c


def tree(recs: dict, order: list[str]) -> list[tuple[int, dict]]:
    """[(depth, rec)] in area -> goal -> to-do order (memos under their area); orphans at the end."""
    kids: dict[str, list[str]] = {}
    for i in order:
        kids.setdefault(recs[i].get("parent") or "", []).append(i)
    out, seen = [], set()

    def walk(i, d):
        if i in seen:
            return
        seen.add(i)
        out.append((d, recs[i]))
        for k in kids.get(i, []):
            walk(k, d + 1)
    for i in sorted((i for i in order if recs[i]["kind"] == "영역"), key=lambda i: recs[i].get("title", "")):
        walk(i, 0)
    for i in order:
        if i not in seen:
            walk(i, 1)
    return out


def proj_name(pj: str) -> str:
    return pj.rstrip("/").split("/")[-1] if pj else ""


def archive_due_projects(r: dict, recs: dict) -> list[str]:
    """A closed goal's Desk project folders still in 1_Projects that no open goal shares."""
    if r["kind"] != "목표" or r.get("state") not in ("done", "dropped"):
        return []
    out = []
    for pj in projects_of(r):
        path = desk_file(pj)
        if not (pj.startswith("1_Projects/") and path is not None and path.exists()):
            continue
        if not any(x["kind"] == "목표" and pj in projects_of(x) and x.get("state") == "open" for x in recs.values()):
            out.append(pj)
    return out


def archive_due(r: dict, recs: dict) -> bool:
    return bool(archive_due_projects(r, recs))
