"""Board comments and the owner's own marks: comment, reply, owner-done, owner-undo."""

from __future__ import annotations

import re
import time

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, ago, die, mask, now
from hermes_cli.work.evaluate import item_name
from hermes_cli.work.items import get
from hermes_cli.work.locale import REPLY_ALIASES, kind, t
from hermes_cli.work.notify import answering_agent, tell, tell_owner_agent
from hermes_cli.work.people import agent, agents, is_owner_word, name, owner
from hermes_cli.work.waits import release_waits, tell_released
from hermes_cli.work.writing import guard_text

OWNER_UNDO_LIMIT = 10 * 60


def reply_hint(r: dict, cid: str) -> str:
    return t("처리한 뒤 work reply {id} {cid} \"<한 일>\" --result …", id=r["id"], cid=cid)


def commentable(recs: dict, i: str) -> dict:
    r = dict(get(recs, i))
    if r["kind"] not in ("목표", "할 일"):
        die(t("댓글은 {goal}·{todo}에만 단다", goal=kind("목표"), todo=kind("할 일")))
    if r.get("state") == "dropped":
        die(t("{id}는 내린 항목이다", id=r["id"]))
    r["comments"] = [dict(c, replies=list(c.get("replies") or [])) for c in r.get("comments") or []]
    return r


def cmd_comment(a) -> None:
    by = owner() if is_owner_word(a.by or owner()) else agent(a.by)
    text = mask(a.text).strip()
    if not text:
        die(t("댓글 글이 비었다"))
    with Lock():
        recs, _ = ledger.read_all()
        r = commentable(recs, a.id)
        n = 1 + max([int(c["cid"][1:]) for c in r["comments"] if re.fullmatch(r"c\d+", c.get("cid", ""))] or [0])
        cid = f"c{n}"
        row = {"cid": cid, "by": by, "at": now(), "text": text, "status": "open", "replies": []}
        if a.via:
            row["via"] = a.via
        if a.answer:
            if not r.get("decide"):
                die(t("{id}에 주인이 정할 것이 없다", id=r["id"]))
            row["kind"] = "answer"
        r["comments"].append(row)
        ledger.append(r, "comment", by)
    if by != answering_agent(r):
        tell_owner_agent(r, t("[대시보드] {who} 댓글 {id} {cid}: {text} — {hint}", who=name(by), id=r["id"], cid=cid,
                              text=" ".join(text.split())[:80], hint=reply_hint(r, cid)))
    print(cid)


def _clear_decision(r: dict) -> None:
    r["decide"] = ""
    for key in ("decide_cid", "decide_choices", "decide_recommend"):
        r.pop(key, None)


def _ask_again(r: dict, text: str, cid: str) -> None:
    """A follow-up question: the old fixed answers belong to the old question."""
    r["decide"], r["decide_cid"] = text, cid
    r.pop("decide_choices", None)
    r.pop("decide_recommend", None)


def _apply_reply(r: dict, c: dict, result: str, text: str, cid: str) -> None:
    if c.get("kind") == "answer":
        # An answer to the owner's decision: 반영함 or 반영 안 함 settles it (the reason stays in the reply);
        # 답변 필요 keeps the decision open with the reply as its new sentence.
        if result == "답변 필요":
            _ask_again(r, text, cid)
        else:
            c["status"] = "resolved"
            _clear_decision(r)
    elif result == "답변 필요":
        if r.get("decide") and r.get("decide_cid") != cid:
            die(t("{id}에 이미 주인이 정할 것이 있다: {decide} — 그 결정이 닫힌 뒤 다시", id=r["id"], decide=r["decide"]))
        _ask_again(r, text, cid)
    else:
        c["status"] = "resolved"
        if r.get("decide_cid") == cid:
            r["decide"] = ""
            r.pop("decide_cid")


def cmd_reply(a) -> None:
    result = REPLY_ALIASES.get(a.result, a.result)
    text = mask(a.text).strip()
    if result == "반영 안 함" and not text:
        die(t("반영 안 함에는 이유 글이 있어야 한다"))
    guard_text("결정 문장" if result == "답변 필요" else "답글", text)
    with Lock():
        recs, _ = ledger.read_all()
        r = commentable(recs, a.id)
        by = agent(a.by) if a.by else answering_agent(r)
        if by not in agents():
            die(t("{id}에 답한 에이전트를 --by로 준다", id=r["id"]))
        c = next((x for x in r["comments"] if x.get("cid") == a.cid), None)
        if not c:
            die(t("{id}에 없는 댓글: {cid}", id=r["id"], cid=a.cid))
        if c.get("status") != "open":
            die(t("{id} {cid}는 이미 해결된 댓글이다", id=r["id"], cid=a.cid))
        _apply_reply(r, c, result, text, a.cid)
        c["replies"].append({"by": by, "at": now(), "text": text, "result": result})
        r["progress_at"] = now()
        ledger.append(r, "reply", by)
    state = t("열어 둠, 주인 결정 기다림") if c["status"] == "open" else t("해결")
    print(f"{r['id']} {a.cid}: {state}")


def cmd_owner_done(a) -> None:
    with Lock():
        recs, _ = ledger.read_all()
        r = dict(get(recs, a.id))
        if r["kind"] not in ("목표", "할 일"):
            die(t("완료 표시는 {goal}·{todo}에만 한다", goal=kind("목표"), todo=kind("할 일")))
        if r.get("state") != "open":
            die(t("{id}는 열린 항목이 아니다", id=r["id"]))
        kids = [x for x in recs.values() if x.get("parent") == r["id"] and x["kind"] == "할 일"
                and x.get("state") == "open"]
        if kids and not a.with_open:
            die(t("{id}에 열린 할 일이 남아 있어 완료로 표시하지 않았다: {kids} — 함께 닫으려면 --with-open", id=r["id"],
                  kids=", ".join(f"{x['id']} {item_name(x)}" for x in kids)))
        at = now()
        where = t("대시보드에서 ") if a.via else ""
        ev = t("{where}주인이 완료로 표시함 ({when})", where=where, when=time.strftime("%m-%d %H:%M", time.localtime(at)))
        done_by = t("확인({who})", who=name(owner()))
        # The open to-dos close first on the same evidence; the goal's mark names them so one undo reopens all.
        for kid in kids:
            ledger.append(dict(kid), "owner_done", owner(), state="done", done_at=at, done_by=done_by,
                          done_evidence=ev, owner_done={"at": at, "via": a.via or "",
                                                        "prev_rev": int(kid.get("rev", 0)), "goal": r["id"]})
        mark = {"at": at, "via": a.via or "", "prev_rev": int(r.get("rev", 0))}
        if kids:
            mark["with"] = [kid["id"] for kid in kids]
        ledger.append(r, "owner_done", owner(), state="done", done_at=at, done_by=done_by, done_evidence=ev,
                      owner_done=mark)
        for x in kids + [r]:
            recs[x["id"]] = dict(x, state="done")
        pairs = release_waits(recs, {x["id"] for x in kids + [r]}, at, owner())
    also = t(" (열린 할 일 {n}개 함께: {ids})", n=len(kids), ids=", ".join(k["id"] for k in kids)) if kids else ""
    if r.get("by") != owner():
        tell_owner_agent(r, t("[대시보드] 주인이 {id}를 완료로 표시함{also}. 이 일로 도는 작업은 멈춰라.", id=r["id"],
                              also=also))
    tell_released(pairs, kids + [r])
    print(t("{id} 끝남(주인)", id=r["id"]) + (" · " + " ".join(k["id"] for k in kids) if kids else ""))


def owner_prev(r: dict, mark: dict) -> dict:
    """The record as it was before the owner's done mark."""
    prev = None
    for row in ledger.rows(r["id"]):
        if not row.get("_meta") and row.get("id") == r["id"] and int(row.get("rev", 0)) == mark["prev_rev"]:
            prev = row
    if prev is None:
        die(t("{id}의 완료 표시 전 기록을 못 찾았다", id=r["id"]))
    return prev


def _undo_marks(recs: dict, r: dict, mark: dict, via: str):
    # To-dos closed with the goal reopen with it, each only while its last record is still that same mark.
    kids = [recs[kid] for kid in mark.get("with") or [] if kid in recs and recs[kid].get("op") == "owner_done"
            and (recs[kid].get("owner_done") or {}).get("at") == mark["at"]]
    for row, prev in [(x, owner_prev(x, x["owner_done"])) for x in kids] + [(r, owner_prev(r, mark))]:
        ledger.append(dict(prev, rev=row.get("rev", 0)), "owner_undo", owner(),
                      owner_undo={"at": now(), "via": via})
    # Waits this mark released come back, each only while its last record is still that release.
    undone = {x["id"] for x in kids + [r]}
    back = [w for w in recs.values() if w.get("op") == "wait_release" and w.get("state") == "open"
            and (w.get("released") or {}).get("item") in undone and w["released"].get("at") == mark["at"]]
    for w in back:
        row = dict(w, wait=w["released"]["wait"])
        row.pop("released")
        ledger.append(row, "wait_restore", owner())
    return kids, back


def cmd_owner_undo(a) -> None:
    with Lock():
        recs, _ = ledger.read_all()
        r = get(recs, a.id)
        mark = r.get("owner_done") or {}
        if r.get("op") != "owner_done" or not mark:
            die(t("{id}의 마지막 기록이 주인의 완료 표시가 아니다", id=r["id"]))
        if now() - int(mark["at"]) > OWNER_UNDO_LIMIT:
            die(t("완료 표시 뒤 10분이 지나 되돌리지 않았다 ({ago} 전). 다시 열려면 work reopen", ago=ago(mark["at"])))
        kids, back = _undo_marks(recs, r, mark, a.via or "")
    also = t(" (함께 닫은 할 일 {n}개도)", n=len(kids)) if kids else ""
    if r.get("by") == owner():
        for w in back:
            tell(w["by"], t("[대시보드] 주인이 '{done}' 완료를 되돌렸다. {name}는 다시 기다린다", done=item_name(r),
                            name=item_name(w)))
        if not back:
            tell_owner_agent(r, t("[대시보드] 주인이 '{done}' 완료를 되돌렸다.", done=item_name(r)))
    else:
        tell_owner_agent(r, t("[대시보드] 주인이 {id} 완료 표시를 되돌림{also}.", id=r["id"], also=also))
    print(t("{id} 다시 열림", id=r["id"]) + (" · " + " ".join(k["id"] for k in kids) if kids else ""))
