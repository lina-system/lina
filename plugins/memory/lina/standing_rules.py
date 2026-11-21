"""The conversation tool. Only the persisted owner turn supplies words to a write."""

from __future__ import annotations

import json
import re
import uuid

from psycopg import Error
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from hermes_state_store import StoreUnavailableError

from . import copy_rules
from .rule_sentences import interpret

TOOL = "standing_rule"
_PROPERTIES = {
    "action": {"enum": ["record", "change", "end", "list", "pattern_offer", "answer_offer"]},
    "rule_id": {"type": "string", "description": "Rule to change or end."},
    "offer_id": {"type": "string"},
    "history": {"type": "boolean"},
    "kind": {"enum": ["conduct", "filter"]},
}
SCHEMA = {
    "name": TOOL,
    "description": (
        "Use for the owner's standing instructions, fact corrections, changes, scope answers, endings, or "
        "requests to list rules. The store supplies this turn's words. Use pattern_offer to suggest a "
        "stored memory pattern and answer_offer for an answer to a displayed offer. In the ordinary "
        "reply include the returned confirmation or refusal and end with any question exactly once."
    ),
    "parameters": {
        "type": "object",
        "properties": _PROPERTIES,
        "required": ["action"],
        "additionalProperties": False,
    },
}
_OWNER_ONLY = "주인이 직접 말한 문장만 규칙으로 남길 수 있어요."


def schema(home):
    if copy_rules.copy_root(home) is None:
        return SCHEMA
    properties = {**_PROPERTIES, "action": {"enum": [*_PROPERTIES["action"]["enum"], "copy_offer", "answer_copy"]}}
    return {
        **SCHEMA,
        "description": SCHEMA["description"] + " On an opted-in copy use copy_offer to review old rules. "
        "Return the question verbatim as the whole reply. Use answer_copy on the owner's next turn.",
        "parameters": {**SCHEMA["parameters"], "properties": properties},
    }


_ALL = {"everyone", "all agents", "all of you", "모두", "전부", "모두에게", "모든 에이전트", "모든 에이전트에게"}
_ME = {"just you", "only you", "you only", "너만", "너한테만", "너에게만"}
_HERE = {"this room", "only here", "in this room", "이 방", "이 방에서만", "여기서만"}
_END = re.compile(
    r"(?:please )?(?:forget|end|retire|remove|drop|stop (?:using|following)) (?:this |that |the )?rule"
    r"|you can bring those again|(?:이|그|저|해당) 규칙(?:은|을)?\s*(?:잊어|그만|끝내|없애|종료|해제)(?:줘| 줘|자|요)?",
    re.IGNORECASE,
)


def _owner_turn(cur, uid, session_id):
    cur.execute(
        """SELECT uid, content, conversation_id, session_id FROM memory.messages
           WHERE uid = %s AND session_id = %s AND agent_id = public.lina_current_agent()
             AND source = 'agent' AND role = 'user' AND speaker = 'owner' AND grade = 'owner'
             AND forgotten_at IS NULL FOR SHARE""", (uid, session_id),
    )
    row = cur.fetchone()
    if not row or not row["content"]:
        raise ValueError(_OWNER_ONLY)
    return row


def _pasted(cur, words):
    """Recognize verbatim agent replies even when pasted without quote metadata."""
    cur.execute(
        """SELECT EXISTS (SELECT FROM memory.messages WHERE source = 'agent' AND speaker = 'agent'
            AND forgotten_at IS NULL AND nullif(btrim(content), '') IS NOT NULL
            AND lower(btrim(content)) = lower(btrim(%s))) AS pasted""", (words,),
    )
    return cur.fetchone()["pasted"]


def _listing(cur, history, place):
    cur.execute(
        """SELECT rule_id, version, status, scope, agent_id, place_ref, kind, statement,
                  said_at, replaced_at, replaced_by, shared_rule_id, filtered_count
           FROM memory.standing_rules WHERE (scope <> 'place' OR place_ref = %s)
             AND (status = 'active' OR %s) ORDER BY rule_id, version""", (place, history),
    )
    return {"rules": cur.fetchall()}


def _place(conversation, gateway_key):
    key = conversation or gateway_key
    return "channel:" + key if key else None


def _head(cur, rule_id):
    cur.execute("SELECT pg_advisory_xact_lock(hashtextextended(%s, 0))", (str(rule_id),))
    cur.execute("SELECT * FROM memory.standing_rule_head(%s)", (rule_id,))
    return cur.fetchone()


def _scope_answer(words, agent, place):
    if words in _ALL:
        return "all", "*", None
    if words in _ME:
        return "agent", f"agent:{agent}", None
    if words in _HERE:
        return "place", "*", place
    return None


def _save(cur, rule_id, uid, kind, scope, target, place, statement, status, match):
    cur.execute(
        """SELECT memory.record_standing_rule(%s,%s,%s,%s,%s,%s,%s,%s,'{}',%s) AS version""",
        (rule_id, uid, kind, scope, target, place, statement, status, Jsonb(match) if match else None),
    )
    return {"rule_id": str(rule_id), "version": cur.fetchone()["version"], "scope": scope}


def _record(cur, conn, args, source, agent, place):
    action, uid, text = args["action"], source["uid"], source["content"]
    rule_id = (uuid.uuid5(uuid.NAMESPACE_URL, f"lina:standing-rule:{uid}") if action == "record"
               else uuid.UUID(str(args.get("rule_id") or "")))
    prior = _head(cur, rule_id)
    if prior and prior["source_uid"] == uid:
        return {"rule_id": str(rule_id), "version": prior["version"], "scope": prior["scope"]}
    if (action == "record" and prior) or (action != "record" and (not prior or prior["status"] != "active")):
        raise ValueError("바꿀 규칙을 찾지 못했어요.")
    words = text.strip().rstrip(".!?。").casefold()
    kind = prior["kind"] if prior else args.get("kind", "conduct")
    if action == "end":
        if not _END.fullmatch(words):
            raise ValueError("끝낼 규칙을 한 문장으로 알려 주세요.")
        result = _save(cur, rule_id, uid, kind, prior["scope"], prior["agent_id"], prior["place_ref"],
                       text, "ended", prior["filter_match"])
        return {**result, "reply": "규칙을 끝냈어요."}

    answer = _scope_answer(words, agent, place) if action == "change" else None
    if answer:
        scope, target, location = answer
        if scope == "place" and not location:
            raise ValueError("어느 장소를 가리키는지 확인할 수 없어요.")
        statement, unclear = prior["statement"], False
    else:
        reading = interpret(text, agent=agent, place=place)
        if reading.refusal:
            return {"refusal": reading.refusal}
        if reading.statement and _pasted(cur, reading.statement):
            raise ValueError(_OWNER_ONLY)
        if reading.fact:
            # The searchable message is the original fact source. Preserve its correction clause
            # separately for mixed turns without creating a second, apparently owner-authored turn.
            cur.execute("""UPDATE memory.messages
                           SET native = coalesce(native, '{}') || jsonb_build_object('fact_correction', %s::text)
                           WHERE uid = %s""", (reading.fact, uid))
        if not reading.statement or not reading.scope:
            return {"fact_uid": uid} if reading.fact else {"refusal": "남길 규칙을 한 문장으로 알려 주세요."}
        scope, target, location = reading.scope, reading.target, reading.place_ref
        statement, unclear = reading.statement, reading.unclear
        if prior and unclear:
            scope, target, location = prior["scope"], prior["agent_id"], prior["place_ref"]
            unclear = False
    result = _save(cur, rule_id, uid, kind, scope, target, location, statement, "active",
                   {"statement": statement} if kind == "filter" else None)
    result["reply"] = "규칙을 남겼어요." if action == "record" else "규칙을 바꿨어요."
    if unclear:
        result["question"] = "모두에게 적용할까요, 저만 지킬까요?"
    elif scope == "agent":
        from . import rule_offers
        offer = rule_offers.shared_offer(conn, rule_id, source["session_id"])
        if offer:
            result.update(offer)
    return result


def handle(provider, args):
    from . import rule_offers
    from tools.registry import tool_error

    if args.keys() - _PROPERTIES.keys():
        return tool_error(_OWNER_ONLY)
    try:
        action = args.get("action")
        if action in {"copy_offer", "answer_copy"}:
            root = copy_rules.copy_root(provider._home)
            if root is None or len(args) != 1:
                return tool_error("이 작업은 명시적으로 선택한 사본에서만 사용할 수 있어요.")
        selected = []
        uid = provider._rule_source_uid
        if not uid:
            return tool_error(_OWNER_ONLY)
        with provider._brain_connection() as conn, conn.transaction(), conn.cursor(row_factory=dict_row) as cur:
            source = _owner_turn(cur, uid, provider._session_id)
            place = _place(source["conversation_id"], provider._init.get("gateway_session_key"))
            if action == "list":
                result = _listing(cur, args.get("history") is True, place)
            elif action in {"record", "change", "end"}:
                if _pasted(cur, source["content"]):
                    raise ValueError(_OWNER_ONLY)
                result = _record(cur, conn, args, source, provider._agent_name(), place)
            elif action == "pattern_offer":
                result = rule_offers.next_pattern_offer(conn, provider._session_id)
            elif action == "answer_offer":
                result = rule_offers.answer_offer(conn, args.get("offer_id"), uid, provider._session_id)
            elif action == "copy_offer":
                result = copy_rules.offer(provider, root, uid)
            elif action == "answer_copy":
                selected = copy_rules.confirm(provider, root, source, cur)
                result = {"recorded": len(selected)}
            else:
                raise ValueError("규칙으로 할 일을 알려 주세요.")
        if selected:
            from hermes_cli.rule_cleanup import cleanup
            from hermes_cli.undo.models import UndoError

            try:
                result["undo_id"] = cleanup(root, selected)
            except (OSError, ValueError, UndoError):
                result["error"] = "규칙 등록 후 사본 정리를 완료하지 못했어요. 되돌리기 기록을 확인해 주세요."
            else:
                provider._copy_rule_offer = None
    except OSError:
        return tool_error("사본 파일을 확인하지 못했어요.")
    except (ValueError, StoreUnavailableError) as exc:
        return tool_error(str(exc))
    except Error:
        return tool_error("규칙 저장소에서 요청을 처리하지 못했어요.")
    return json.dumps(result or {}, ensure_ascii=False, default=str)
