"""Lines to agents: the reminders ``work check --notify`` sends (each names the next command), due-date reminders,
the daily tidy list and the one-line notices of other writes. A line goes through ``work.notice.command`` (argv,
``{agent}`` filled in, the body on stdin) or, unset, into ``$LINA_HOME/work/notices/<agent>.log``."""

from __future__ import annotations

import datetime
import subprocess

from hermes_cli.work.common import iso, load_json, now, paths, save_json, warn
from hermes_cli.work.env import current
from hermes_cli.work.evaluate import item_name
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.locale import t
from hermes_cli.work.people import agents, owner

BOT_GAP, ITEM_GAP = 30 * 60, 6 * 3600
DUE_GAP = 24 * 3600
DUE_MORNING = (7, 12)  # local hours when due-over and due-today go out, once a day
DUE_STATUSES = {"due_over", "due_today"}


def _notice_argv(agent: str):
    notice = current().section("work").get("notice")
    argv = notice.get("command") if isinstance(notice, dict) else None
    return [str(a).replace("{agent}", agent) for a in argv] if isinstance(argv, list) and argv else None


def tell(agent: str, line: str) -> None:
    """One line to an agent; the owner gets nothing. A failure never undoes the write."""
    if agent not in agents():
        return
    argv = _notice_argv(agent)
    try:
        if argv:
            subprocess.run(argv, input=line, text=True, encoding="utf-8", capture_output=True, timeout=60,
                           check=False)
            return
        folder = paths()["notices"]
        folder.mkdir(parents=True, exist_ok=True)
        with open(folder / f"{agent}.log", "a", encoding="utf-8") as fh:
            fh.write(f"{iso()} {line}\n")
    except (OSError, subprocess.SubprocessError) as exc:
        warn(t("{agent}에게 알림 못 보냄 ({kind})", agent=agent, kind=type(exc).__name__))


def answering_agent(r: dict) -> str:
    """The agent that answers for an item: its owner agent, or for the owner's own to-do the agent that asked."""
    return r.get("from", "") if r.get("by") == owner() else r["by"]


def tell_owner_agent(r: dict, line: str) -> None:
    tell(answering_agent(r), line)


def next_command(r: dict, c: dict, agent: str) -> str:
    """The one command that moves the item on, for a reminder."""
    i, chk = r["id"], c.get("check") or {}
    if r.get("evidence"):
        return f'work confirm {i} --by {agent} --seen "<{t("무엇을 확인했나")}>"'
    if r.get("notes") and chk and chk.get("rc") != 0:
        return f"work evidence {i} <ref>  ·  work wait {i} --on \"…\" --next <{t('날짜')}>"
    if c["status"] == "check_left":
        return f"work check {i}"
    if c["status"] == "bad":
        return f"work show {i}  →  work check {i}"
    if c["status"] in ("due_over", "due_today"):
        return f"work edit {i} --due YYYY-MM-DD  ·  work note {i} \"…\""
    if not (r.get("links") or r.get("session")) and kind_now(r["kind"]) == TODO:
        return f"lina work run submit {i} --brief <brief.md>"
    if not r.get("links") and r["kind"] == "목표":
        return f"work add 할 일 --goal {i} …"
    return f"work note {i} \"…\"  ·  work wait {i} --on \"…\" --next <{t('날짜')}>"


def alert_line(r: dict, c: dict, agent: str) -> str:
    if c["status"] in ("due_over", "due_today"):
        return c["line"] + " — " + t("다음: {cmd}", cmd=next_command(r, c, agent))
    reason = (c["reasons"] or [c["line"]])[0]
    return t("{name}({id}): {reason} — 다음: {cmd}", name=item_name(r), id=r["id"], reason=reason,
             cmd=next_command(r, c, agent))


def _pending_alerts(items_alert, nst: dict, at: int) -> dict[str, list]:
    by_agent: dict[str, list] = {}
    for r, c in items_alert:
        # a due reminder has its own key (id:due), so it and the item's other reminder never reset each other's gap
        last = nst["items"].get(c.get("key", r["id"]), {})
        sig = c["status"] + "|" + (c["reasons"][0] if c["reasons"] else "")
        # due items are gated once a day per agent in notify(); do not filter them by ITEM_GAP
        if c["status"] not in DUE_STATUSES:
            if at - last.get("at", 0) < ITEM_GAP and last.get("sig", "").split("|")[0] == c["status"]:
                continue
        if answering_agent(r) in agents():
            by_agent.setdefault(answering_agent(r), []).append((r, c, sig))
    return by_agent


def notify(items_alert, recs, dry: bool = False) -> list[str]:
    """items_alert: [(rec, computed)] already filtered.

    Due-over/due-today: one digest per agent per day, all items, not blocked by the 30 min agent gap.
    Other reminders: agent 30 min, item 6 h, at most 6 lines in the message.
    """
    nst = load_json(paths()["notify"], {"bots": {}, "items": {}})
    at, sent = now(), []
    pending = _pending_alerts(items_alert, nst, at)
    due_bots = nst.setdefault("due_bots", {})
    for agent, lst in pending.items():
        due = [(r, c, sig) for r, c, sig in lst if c["status"] in DUE_STATUSES]
        if not due or at - due_bots.get(agent, 0) < DUE_GAP:
            continue
        msg = t("[대시보드] 맡은 항목 {n}건 — 다음 할 일", n=len(due)) + "\n" + \
            "\n".join("- " + alert_line(r, c, agent) for r, c, _ in due)
        if dry:
            print(f"[dry] due -> {agent}: {msg[:200]}")
        else:
            tell(agent, msg)
        for r, c, sig in due:
            nst["items"][c.get("key", r["id"])] = {"at": at, "sig": sig}
        due_bots[agent] = at
        sent.append(f"{agent}:due:{len(due)}")
    for agent, lst in pending.items():
        other = [(r, c, sig) for r, c, sig in lst if c["status"] not in DUE_STATUSES]
        if not other or at - nst["bots"].get(agent, 0) < BOT_GAP:
            continue
        msg = t("[대시보드] 맡은 항목 {n}건 — 다음 할 일", n=len(other)) + "\n" + \
            "\n".join("- " + alert_line(r, c, agent) for r, c, _ in other[:6])
        if dry:
            print(f"[dry] notice -> {agent}: {msg[:200]}")
        else:
            tell(agent, msg)
        for r, c, sig in other:
            nst["items"][c.get("key", r["id"])] = {"at": at, "sig": sig}
        nst["bots"][agent] = at
        sent.append(f"{agent}:{len(other)}")
    if not dry:
        save_json(paths()["notify"], nst)
    return sent


def notify_tidy(goals, dry: bool = False) -> list[str]:
    """One daily planning cleanup notice per agent, independent of other reminders."""
    nst = load_json(paths()["notify"], {"bots": {}, "items": {}})
    daily, at, sent = nst.setdefault("tidy_bots", {}), now(), []
    groups: dict[str, list] = {}
    for r in goals:
        groups.setdefault(r["by"], []).append(r)
    for agent, group in groups.items():
        if agent in daily and at - daily[agent] < 86400:
            continue
        body = t("[대시보드] 정리 대상 목표 — 할 일을 붙이거나(work add 할 일 --goal <id>) 주인에게 물어 내린다") + "\n" + \
            "\n".join("- " + r["id"] + " " + item_name(r) for r in group)
        if dry:
            print(f"[dry] tidy {agent}: {len(group)}")
        else:
            tell(agent, body)
        daily[agent] = at
        sent.append(f"{agent}:tidy:{len(group)}")
    if not dry:
        save_json(paths()["notify"], nst)
    return sent


def _today():
    tz = current().section("work").get("timezone")
    if tz:
        from zoneinfo import ZoneInfo
        return datetime.datetime.fromtimestamp(now(), ZoneInfo(str(tz)))
    return datetime.datetime.fromtimestamp(now()).astimezone()


def owner_today() -> datetime.date:
    """Today on the owner's calendar (``work.timezone``, default the host's): the day due dates are read against."""
    return _today().date()


def due_alerts(items) -> list[tuple[dict, dict]]:
    """[(rec, computed)] for open goals and to-dos past their due date or due today, only in the morning window.
    A due date is a day on the owner's calendar (``work.timezone``, default the host's)."""
    kt = _today()
    today, morning = kt.strftime("%Y-%m-%d"), DUE_MORNING[0] <= kt.hour < DUE_MORNING[1]
    out = []
    for r in items:
        d = r.get("due") or ""
        if not d or r.get("state") != "open" or kind_now(r["kind"]) not in ("목표", TODO):
            continue
        # overdue shares the morning window with due-today, so the 15-min checker does not drip them all day
        if not morning:
            continue
        if d < today:
            code, line = "due_over", t("마감일 지남: {name}({id}) {due}", name=item_name(r), id=r["id"], due=d)
        elif d == today:
            code, line = "due_today", t("오늘 마감: {name}({id}) {due}", name=item_name(r), id=r["id"], due=d)
        else:
            continue
        out.append((r, {"status": code, "line": line, "reasons": [], "key": r["id"] + ":due"}))
    return out
