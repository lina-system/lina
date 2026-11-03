"""Writes on one item: link, unlink, note, wait, evidence, confirm, drop, reopen, undo."""

from __future__ import annotations

import sys

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, die, mask, now, writer_profile
from hermes_cli.work.items import due_hint, get, mutate, parse_day
from hermes_cli.work.links import norm_link
from hermes_cli.work.locale import t
from hermes_cli.work.notify import answering_agent
from hermes_cli.work.people import agent, name, owner
from hermes_cli.work.waits import release_waits, tell_released
from hermes_cli.work.writing import guard_text

SEEN_MIN = 10
OWNER_WORDS = ("주인", "owner")


def cmd_link(a) -> None:
    def f(r):
        for link in a.links:
            nl = norm_link(link, r["by"])
            if nl not in r["links"]:
                r["links"].append(nl)
            if nl.startswith("omo:"):
                r.pop("omo_next_at", None)
        r["progress_at"] = now()
    r = mutate(a.id, "link", f, a.as_)
    print(t("{id}: 이슈 {n}개", id=r["id"], n=len(r["links"])))


def cmd_unlink(a) -> None:
    def f(r):
        drop = {norm_link(link, r["by"]) for link in a.links}
        r["links"] = [x for x in r["links"] if x not in drop]
    r = mutate(a.id, "unlink", f, a.as_)
    print(t("{id}: 이슈 {n}개", id=r["id"], n=len(r["links"])))


def close_command(r: dict) -> str:
    """The exact command that closes an item whose evidence waits for its confirm."""
    return f'work confirm {r["id"]} --by {r["by"]} --seen "<{t("직접 확인한 것")}>"'


def cmd_note(a) -> None:
    guard_text("진행 메모", a.text)
    writer = []

    def f(r):
        writer.append(agent(a.as_) if a.as_ else answering_agent(r))
        r.setdefault("notes", []).append({"ts": now(), "by": writer[0], "text": a.text})
        r["notes"] = r["notes"][-30:]
        r["progress_at"] = now()
    r = mutate(a.id, "note", f, a.as_)
    print("ok")
    # The owning agent keeps noting "done" on an item whose evidence waits for its confirm: name the command.
    if r.get("state") == "open" and r.get("evidence") and r["by"] != owner() and writer[0] == r["by"]:
        print(t("증거가 붙어 있다. 끝났으면: {cmd}", cmd=close_command(r)))
    due_hint(r, (a.text,))


def set_decide(r: dict, a) -> None:
    """The owner's decision with its fixed answers; an empty sentence clears the decision and its answers."""
    text = (a.decide or "").strip()
    choices = [c.strip() for c in a.choice or [] if c.strip()]
    if not text and (choices or a.recommend):
        die(t("--choice·--recommend는 --decide \"<문장>\"과 함께 쓴다"))
    if a.recommend and a.recommend.strip() not in choices:
        die(t("--recommend는 --choice 가운데 하나다"))
    guard_text("결정 문장", text)
    for c in choices:
        guard_text("고를 답", c)
    r["decide"] = text
    r.pop("decide_recommend", None)
    if choices:
        r["decide_choices"] = list(dict.fromkeys(choices))
        if a.recommend:
            r["decide_recommend"] = a.recommend.strip()
    else:
        r.pop("decide_choices", None)


def _wait_on_item(r: dict, a) -> None:
    recs, _ = ledger.read_all()
    target = recs.get(a.on_item.strip().upper())
    if not target or target["kind"] != "할 일" or target.get("state") != "open" or target["id"] == r["id"]:
        die(t("--on-item은 다른 열린 할 일 id다: {v}", v=a.on_item))
    if r["kind"] != "할 일" or owner() not in (r["by"], target["by"]):
        die(t("--on-item: 에이전트의 할 일은 주인 할 일을, 주인 할 일은 열린 할 일을 기다린다"))
    r["wait"] = {"item": target["id"], "since": now()}


def _wait_plain(r: dict, a) -> None:
    if a.clear:
        r["wait"] = {}
    elif a.hold:
        if a.next:
            die(t("--hold와 --next는 함께 쓸 수 없다"))
        guard_text("멈춤 이유", a.on)
        r["wait"] = {"on": a.on or "", "hold": True, "since": now()}
    else:
        if not (a.on and a.next):
            die(t("--on \"<누구·무엇>\" --next <날짜> 가 필요하다"))
        guard_text("대기 문장", a.on)
        r["wait"] = {"on": a.on, "next": parse_day(a.next), "since": now()}


def _owner_named(text: str) -> bool:
    from hermes_cli.work.env import current
    return any(w and w in text for w in (current().owner_name, *OWNER_WORDS))


def cmd_wait(a) -> None:
    if a.on_item and (a.on or a.next or a.hold or a.clear or a.decide is not None or a.choice or a.recommend):
        die(t("--on-item은 다른 기다림 옵션과 함께 쓰지 않는다"))

    def f(r):
        r["progress_at"] = now()
        if a.on_item:
            return _wait_on_item(r, a)
        if a.on and r["by"] != owner() and _owner_named(a.on) and not a.clear:
            print("work: " + t("안내 — 주인이 할 일이면 주인 할 일로 만들고(work add 할 일 --by {owner} --from <나>) "
                               "work wait {id} --on-item <주인 할 일 id>로 기다리면 주인이 끝낼 때 저절로 풀린다",
                               owner=owner(), id=r["id"]), file=sys.stderr)
        if a.decide is not None:
            set_decide(r, a)
            if not (a.clear or a.hold or a.on or a.next):
                return None
        elif a.choice or a.recommend:
            die(t("--choice·--recommend는 --decide \"<문장>\"과 함께 쓴다"))
        return _wait_plain(r, a)
    mutate(a.id, "wait", f, a.as_)
    print("ok")


def cmd_evidence(a) -> None:
    def f(r):
        for ref in a.refs:
            ref = norm_link(ref, r["by"]) if not ref.startswith("evidence:") else ref
            r.setdefault("evidence", []).append({"ts": now(), "ref": ref, "by": agent(a.as_) if a.as_ else r["by"]})
        r["progress_at"] = now()
    r = mutate(a.id, "evidence", f, a.as_)
    print(t("ok — 검사기가 증거를 확인하면 끝남이 된다. 기계로 못 보는 증거면: {cmd}", cmd=close_command(r)))


def _confirm_words(r: dict, a) -> tuple[str, str]:
    if a.quote:
        return name(owner()), f"{name(owner())}: {a.quote}"
    if r.get("by") == owner():
        die(t("{id}는 주인 할 일이다. 주인이 끝났다고 한 말로 닫는다: --quote \"<주인이 한 말>\"", id=r["id"]))
    if not a.by:
        die(t("--quote \"<주인이 끝났다고 한 말>\" 또는 --by <에이전트> --seen \"<직접 확인한 것>\""))
    seen = (a.seen or "").strip()
    if len(seen) < SEEN_MIN:
        die(t("--seen \"<직접 확인한 것>\"이 필요하다({n}자 이상). 무엇을 확인했는지 남긴다", n=SEEN_MIN))
    cb = agent(a.by)
    if cb == r["by"]:
        return t("{who}, 맡은 봇", who=name(cb)), t("{who} 직접 확인: {seen}", who=name(cb), seen=seen)
    return name(cb), t("{who} 확인: {seen}", who=name(cb), seen=seen)


def cmd_confirm(a) -> None:
    with Lock():
        recs, _ = ledger.read_all()
        r = dict(get(recs, a.id))
        if r.get("state") != "open":
            die(t("{id}는 열린 항목이 아니다", id=r["id"]))
        who, ev = _confirm_words(r, a)
        at = now()
        ledger.append(r, "done", a.by or owner(), state="done", done_at=at, done_by=t("확인({who})", who=who),
                      done_evidence=mask(ev)[:400])
        recs[r["id"]] = dict(r, state="done")
        pairs = release_waits(recs, {r["id"]}, at, a.by or owner())
    # The agent that ran confirm on the owner's words is the one that asked: it needs no line about its own write.
    tell_released(pairs, requester=False)
    print(t("{id} 끝남", id=r["id"]))


def cmd_drop(a) -> None:
    if not a.quote:
        die(t("--quote \"<주인이 취소한 말 그대로>\" 가 필요하다. 에이전트가 스스로 내리지 않는다"))

    def f(r):
        r["state"], r["dropped_quote"], r["dropped_at"] = "dropped", a.quote, now()
    mutate(a.id, "drop", f, a.as_)
    print(t("내림"))


def cmd_reopen(a) -> None:
    def f(r):
        if r.get("state") == "open":
            die(t("이미 열려 있다"))
        r["state"] = "open"
        r.setdefault("notes", []).append({"ts": now(), "by": agent(a.as_) if a.as_ else r["by"],
                                          "text": t("다시 엶: {why}", why=a.why)})
        r["progress_at"] = now()
    mutate(a.id, "reopen", f, a.as_)
    print(t("다시 엶"))


def cmd_undo(a) -> None:
    """Append an earlier revision as the next one (history is never rewritten); only the item's agent or the owner."""
    if not a.why.strip():
        die(t("undo에는 비지 않은 --why가 필요하다"))
    with Lock():
        recs, _ = ledger.read_all()
        current_rec = get(recs, a.id)
        writer = writer_profile()
        if writer not in (owner(), current_rec.get("by")) and not (a.as_ and agent(a.as_) == current_rec.get("by")):
            die(t("맡은 에이전트나 주인만 되돌린다"))
        revision = int(current_rec.get("rev", 0))
        if a.expect is not None and revision != a.expect:
            die(t("그 뒤에 항목이 바뀌었다"))
        history = [row for row in ledger.rows(current_rec["id"]) if not row.get("_meta")
                   and row.get("id") == current_rec["id"] and 0 < int(row.get("rev", 0)) < revision]
        candidates = [r for r in history if a.to is None or r["rev"] == a.to]
        if not candidates:
            die(t("맞는 이전 판이 없다"))
        target = max(candidates, key=lambda r: r["rev"])
        row = dict(target, rev=revision, undo_to=target["rev"], undo_why=a.why.strip())
        ledger.append(row, "undo", a.as_ or writer)
    print(t("{id}: {rev}판으로 되돌림", id=current_rec["id"], rev=target["rev"]))
