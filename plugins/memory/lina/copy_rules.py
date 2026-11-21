"""Bind a copied-home inventory to the list actually shown in this conversation."""

from __future__ import annotations

import re
import stat
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING, Final

from .rule_sentences import interpret

if TYPE_CHECKING:
    from hermes_cli.rule_census import Candidate

_QUESTION: Final = "규칙으로 옮길 번호를 골라 주세요. 예를 들어 ‘2번만’, ‘1, 3’, ‘모두’라고 답할 수 있어요."


def copy_root(home: str) -> Path | None:
    if not home:
        return None
    profile = Path(home)
    if not profile.is_absolute() or profile.resolve() != profile:
        return None
    root = profile.parent.parent if profile.parent.name == "profiles" else profile
    try:
        marker = (root / ".standing-rule-copy").lstat()
    except FileNotFoundError:
        return None
    return root if stat.S_ISREG(marker.st_mode) and marker.st_size == 0 else None


@dataclass(frozen=True)
class Offer:
    root: Path
    session: str
    request: str
    items: list[Candidate]
    question: str


def _eligible(item: Candidate) -> bool:
    if item.kind == "filter":
        return bool(item.filter_match)
    reading = interpret(item.statement, agent=item.agent, place=None)
    return bool(reading.statement and not reading.fact and not reading.refusal)


def offer(provider, root, uid):
    from hermes_cli.rule_census import MAX_OWNER_ITEMS, candidates, owner_list

    found = candidates(root, provider._agent_name())
    items = [item for item in found if _eligible(item)]
    lines = [owner_list(items)]
    visible = items[:MAX_OWNER_ITEMS]
    if remaining := len(items) - len(visible):
        lines.append(f"이번 목록 뒤에 후보 {remaining}건이 더 있어요.")
    if deferred := len(found) - len(items):
        lines.append(f"범위나 뜻을 다시 말해야 하는 후보 {deferred}건은 원문에 남겨요.")
    question = "\n".join([*lines, _QUESTION])
    provider._copy_rule_offer = Offer(root, provider._session_id, uid, visible, question)
    return {"question": question} if items else {"count": 0, "deferred": deferred}


def _selection(text: str, count: int) -> list[int]:
    words = text.strip().casefold().removesuffix(".")
    if words in {"all", "all of them", "confirm all", "모두", "전부", "모두 확정"}:
        return list(range(count))
    # Accept the whole reply or none. A number inside a refusal is never a yes.
    match = re.fullmatch(
        r"(?:confirm |only |numbers? )?([1-9]\d?(?:번)?(?:\s*(?:,|and|와|과|하고|이랑|랑)\s*[1-9]\d?(?:번)?)*)"
        r"(?:만(?: 확정)?| 확정| only)?", words,
    )
    if not match:
        return []
    indices = sorted({int(number) - 1 for number in re.findall(r"\d+", match[1])})
    return indices if indices[-1] < count else []


def _displayed(cur, pending: Offer, answer_uid: str) -> bool:
    cur.execute(
        """SELECT m.uid, m.role, m.speaker, m.grade, m.content, m.forgotten_at,
                  EXISTS (SELECT FROM memory.message_parts p WHERE p.message_id=m.message_id
                          AND p.kind='reply_quote') AS quoted
           FROM memory.messages m WHERE m.agent_id=public.lina_current_agent()
             AND m.session_id=%s AND m.source='agent'
             AND (m.role='user' OR (m.role='assistant' AND coalesce(m.content,'')<>''
                  AND coalesce(m.tool_calls,'null') IN ('null','[]')))
           ORDER BY m.message_id DESC LIMIT 3 FOR SHARE""", (pending.session,),
    )
    turns = cur.fetchall()
    if len(turns) != 3 or any(row["forgotten_at"] or row["quoted"] for row in turns):
        return False
    answer, shown, request = turns
    return (answer["uid"] == answer_uid and request["uid"] == pending.request
            and request["speaker"] == request["grade"] == "owner"
            and shown["role"] == "assistant" and shown["speaker"] == shown["grade"] == "agent"
            and shown["content"].strip() == pending.question.strip())


def confirm(provider, root, source, cur):
    from hermes_cli.rule_census import candidates

    from .standing_rules import _head, _pasted, _save

    pending = provider._copy_rule_offer
    if not pending or (pending.root, pending.session) != (root, provider._session_id):
        return []
    indices = _selection(source["content"], len(pending.items))
    if not indices or not _displayed(cur, pending, source["uid"]) or _pasted(cur, source["content"]):
        return []
    selected = [pending.items[i] for i in indices]
    current = candidates(root, provider._agent_name())
    if not all(item in current for item in selected):
        raise ValueError("원문이 바뀌었어요. 사본 목록을 다시 확인해 주세요.")
    for index, item in zip(indices, selected, strict=True):
        scope, place = item.scope, item.place_ref
        target = f"agent:{item.agent}" if scope == "agent" else "*"
        if item.kind == "conduct":
            reading = interpret(item.statement, agent=item.agent, place=None)
            if reading.scope and not reading.unclear:
                scope, target, place = reading.scope, reading.target, reading.place_ref
        # The same sentence can occupy two numbered slots, even in the same file.
        # A retry of this owner turn must address the very same rows.
        key = f"lina:copied-rule:{pending.request}:{source['uid']}:{index}"
        rule = uuid.uuid5(uuid.NAMESPACE_URL, key)
        head = _head(cur, rule)
        if head:
            if (head["status"] != "active" or head["statement"] != item.statement
                    or head["source_uid"] != source["uid"]):
                raise ValueError("기존 규칙이 바뀌었어요. 사본 원문을 유지했어요.")
        else:
            _save(cur, rule, source["uid"], item.kind, scope, target, place,
                  item.statement, "active", item.filter_match)
    return selected
