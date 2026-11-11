"""Goal cards and work cards (#107) in the record: a goal card carries its ledger goal id, a work card lists the
ledger to-dos it covers. Both are ``kanban:<card>`` links on the record (so the board, #109 gates and #112 read them
from the ``link`` table) plus one comment on the card naming the ids, written through the kanban API."""

from __future__ import annotations

from hermes_cli.work import ledger
from hermes_cli.work.common import die, warn
from hermes_cli.work.evaluate import item_name
from hermes_cli.work.items import get, mutate
from hermes_cli.work.kinds import TODO, kind_now
from hermes_cli.work.links import kanban_parts, norm_link
from hermes_cli.work.locale import kind, t
from hermes_cli.work.people import owner
from hermes_cli.work.run import card_todos

AUTHOR = "lina-work"


def card_link(card: str) -> str:
    return norm_link(card if card.startswith("kanban:") else "kanban:" + card, owner())


def _comment(link: str, body: str) -> None:
    from hermes_cli import kanban_db as kb
    from hermes_cli.kanban_db_connect import connect_closing
    board, card = kanban_parts(link)
    try:
        with connect_closing(board=board or None) as conn:
            if kb.get_task(conn, card) is None:
                warn(t("칸반에 없는 카드: {card} (기록에는 붙였다)", card=card))
                return
            kb.add_comment(conn, card, AUTHOR, body)
    except (OSError, ValueError, RuntimeError) as exc:
        warn(t("카드에 적지 못했다({kind}) — 기록에는 붙였다", kind=type(exc).__name__))


def _link_items(ids: list[str], link: str, want: str, as_) -> list[dict]:
    recs, _ = ledger.read_all()
    for i in ids:
        if kind_now(get(recs, i)["kind"]) != want:
            die(t("{id}는 {kind}이 아니다", id=i, kind=kind(want)))
    out = []
    for i in ids:
        def f(r):
            if link not in r["links"]:
                r["links"].append(link)
        out.append(mutate(i, "link", f, as_))
    return out


def goal_of(link: str):
    recs, order = ledger.read_all()
    return next((recs[i] for i in order if recs[i]["kind"] == "목표" and link in (recs[i].get("links") or [])), None)


def link_card(card: str, *, goal: str | None = None, todos: list[str] | None = None, as_=None) -> list[str]:
    """``work card link <card> [--goal G] [--todo T…]`` without the parser: the ``kanban:<card>`` links on the record
    and one comment on the card per part. Returns the output lines."""
    link = card_link(card)
    if not (goal or todos):
        die(t("--goal <목표 id> 또는 --todo <할 일 id>… 를 준다"))
    lines = []
    if goal:
        g = _link_items([goal.upper()], link, "목표", as_)[0]
        _comment(link, t("기록의 목표: {id} {name}", id=g["id"], name=item_name(g)))
        lines.append(t("{card}: 목표 {id}", card=link[7:], id=g["id"]))
    if todos:
        rs = _link_items([x.upper() for x in todos], link, TODO, as_)
        covered = card_todos(link[7:])
        _comment(link, t("기록의 할 일: {ids}", ids=", ".join(covered)))
        lines.append(t("{card}: 할 일 {ids}", card=link[7:], ids=", ".join(r["id"] for r in rs)))
    return lines


def cmd_card_link(a) -> None:
    for line in link_card(a.card, goal=a.goal, todos=a.todo, as_=a.as_):
        print(line)


def cmd_card_show(a) -> None:
    link = card_link(a.card)
    g = goal_of(link)
    print(t("카드 {card}", card=link[7:]) + (" · " + t("목표 {id} {name}", id=g["id"], name=item_name(g)) if g else ""))
    recs, _ = ledger.read_all()
    for i in card_todos(link[7:]):
        print(f"  {i} · {item_name(recs[i])}")
