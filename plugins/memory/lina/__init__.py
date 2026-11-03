"""lina-memory: the memory layer in the core's memory-provider slot (``memory.provider: lina-memory``, lina#20).

Each finished turn of its agent is received once and routed by the policy table (``policy.yaml``). LCM-X gets it
where LCM-X's own ``post_llm_call`` hook fires: lina-memory claims its session (``lcm.claim_session``), and the
hook hands the turn to ``_take_lcm_turn`` instead of ingesting it itself (``lcm.py``). ``sync_turn``, called once
per finished turn by ``MemoryManager.sync_all``, builds one turn record (person, agent and conversation ids from
the identity registry, the turn's message uids, the route it arrived on, platform and timestamps) and hands it to
the Honcho adapter (``plugins/memory/honcho``, which keeps its code and is driven only from here).
Every other provider call (recall, tools, session lifecycle) goes to the Honcho adapter when Honcho is configured.
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any, Optional

from agent.memory_provider import MemoryProvider, RecallStatus, ctx_bound

from . import lcm
from .identity import IdentityRegistry, SourceKey
from .policy import load_policy, signals_for_turn
from .record import TurnRecord, build_record, turn_rows

logger = logging.getLogger(__name__)

PROVIDER_NAME = "lina-memory"
# How long session end and shutdown wait for the turns still queued for Honcho and the registry.
_DRAIN_TIMEOUT_S = 10.0
# Surfaces where the machine's owner is the one typing: no gateway user id names them.
_LOCAL_PLATFORMS = frozenset({"cli", "tui", "local", "desktop", "acp", "api_server", ""})


def _honcho_provider_class():
    from plugins.memory import import_provider_module
    return import_provider_module("honcho").HonchoMemoryProvider


class LinaMemoryProvider(MemoryProvider):
    def __init__(self) -> None:
        self._honcho = _honcho_provider_class()(card_in_prompt=True)
        self._honcho_on = False
        self._policy = load_policy()
        self._registry = IdentityRegistry()
        self._store = None
        self._home = ""
        self._init: dict[str, Any] = {}
        self._worker = None
        self._session_id = ""

    @property
    def name(self) -> str:
        return PROVIDER_NAME

    def is_available(self) -> bool:
        return True

    def initialize(self, session_id: str, **kwargs) -> None:
        self._home = str(kwargs.get("hermes_home") or "")
        self._init = {key: str(kwargs.get(key) or "") for key in (
            "agent_context", "platform", "user_id", "gateway_session_key")}
        self._honcho_on = self._honcho.is_available()
        self._claim(session_id)
        if self._honcho_on:
            self._honcho.initialize(session_id, **kwargs)

    def _claim(self, session_id: str) -> None:
        if self._session_id:
            lcm.release_session(self._session_id, self._take_lcm_turn)
        self._session_id = session_id or ""
        lcm.claim_session(self._session_id, self._take_lcm_turn)

    # ── the one receive path ──

    def _take_lcm_turn(self, history, *, session_id: str, turn_start: Optional[int] = None) -> None:
        """LCM-X's ``post_llm_call`` hook hands this session's finished turn here (the context-engine loader's
        ``turn_handover_for``), on the turn thread, with the unredacted transcript; lina-memory routes it and hands it
        to the session's engine.
        A failure is logged with its traceback and never reaches the turn or the Honcho delivery."""
        try:
            rule = self._policy.rule(self._policy.route_for(signals_for_turn(
                list(history), agent_context=self._init.get("agent_context", ""),
                platform=self._init.get("platform", ""), turn_author=None, turn_start=turn_start)))
            if rule.lcm:
                lcm.hand_turn(history, session_id=session_id)
        except Exception:  # health: allow BLE001 -- the local engine failing must not cost Honcho the turn
            logger.warning("lina-memory could not hand a turn of session %s to LCM-X", session_id, exc_info=True)

    def sync_turn(self, user_content: str, assistant_content: str, *, session_id: str = "",
                  messages: Optional[list[dict[str, Any]]] = None, turn_author: Optional[dict[str, Any]] = None,
                  local_messages: Optional[list[dict[str, Any]]] = None, turn_start: Optional[int] = None) -> None:
        """Called on the turn thread (``local_messages`` makes the manager do that), after LCM-X's hook handed the
        turn to ``_take_lcm_turn``. The record (identity lookups in the store), the Honcho write (the
        egress-redacted ``messages`` and turn text) and the conversation fill run on this provider's own worker,
        one turn after another.

        Who wrote the turn and the Honcho session it belongs to are fixed here: by the time the worker runs, the
        next turn may have started and Honcho's own per-turn state names that turn's author. The turn starts at
        ``turn_start``, the row the runtime fixed when the turn began; a ``/steer`` or a stop-gate note it appended later
        is in the record, never its input or its route."""
        history = local_messages if local_messages is not None else list(messages or ())
        author = dict(turn_author) if isinstance(turn_author, dict) else {}
        honcho_session = self._honcho.session_key if self._honcho_on else ""
        rule = self._policy.rule(self._policy.route_for(signals_for_turn(
            history, agent_context=self._init.get("agent_context", ""), platform=self._init.get("platform", ""),
            turn_author=author or None, turn_start=turn_start)))
        lcm_conversation = lcm.conversation_id(session_id)

        def deliver() -> None:
            record = self._record(rule, history, session_id=session_id, author=author, turn_start=turn_start,
                                  honcho_session=honcho_session, lcm_conversation=lcm_conversation)
            if record.honcho and self._honcho_on:
                self._honcho.sync_turn(user_content, assistant_content, session_id=session_id, turn_author=author,
                                       messages=messages, turn_record=record, session_key=honcho_session)
            self._fill_conversation(record)

        self._submit(deliver)

    def _submit(self, fn) -> None:
        if self._worker is None:
            from tools.daemon_pool import DaemonThreadPoolExecutor
            self._worker = DaemonThreadPoolExecutor(max_workers=1, thread_name_prefix="lina-memory")
        future = self._worker.submit(ctx_bound(fn))
        future.add_done_callback(lambda done: done.exception() and logger.warning(
            "lina-memory could not deliver a turn: %s", done.exception()))

    def _drain(self) -> None:
        if self._worker is not None:
            try:
                self._worker.submit(lambda: None).result(timeout=_DRAIN_TIMEOUT_S)
            except Exception as exc:  # health: allow BLE001 -- a stuck delivery must not hold session end
                logger.warning("lina-memory: turns still queued at session end: %s", exc)

    def _record(self, rule, history, *, session_id: str, author: dict[str, Any], turn_start: Optional[int],
                honcho_session: str, lcm_conversation: Optional[str]) -> TurnRecord:
        honcho_keys = self._honcho.source_keys(author, session_key=honcho_session) if self._honcho_on else {}
        agent = self._agent_name()
        person_id = None if author.get("is_bot") else self._registry.resolve(
            "person", self._person_keys(author, honcho_keys))
        agent_keys: list[SourceKey] = [("profile", agent), ("honcho_peer", honcho_keys.get("agent", ""))]
        return build_record(
            rule, turn_rows(history, turn_start), store_uid=self._store_uid, person_id=person_id,
            agent_id=self._registry.resolve("agent", agent_keys) or f"agent:{agent}",
            conversation_id=self._registry.resolve("conversation", self._conversation_keys(
                session_id, honcho_keys, lcm_conversation)) or "", session_id=session_id, platform=self._init.get("platform", ""),
            author_id=str(author.get("id") or "") or None,
        )

    def _person_keys(self, author: dict[str, Any], honcho_keys: dict[str, str]) -> list[SourceKey]:
        platform = self._init.get("platform", "")
        user_id = str(author.get("id") or "") or self._init.get("user_id", "")
        keys: list[SourceKey] = [("gateway_user", f"{platform}:{user_id}" if user_id else "")]
        if not user_id and platform in _LOCAL_PLATFORMS:
            keys.append(("local_user", "owner"))
        keys.append(("honcho_peer", honcho_keys.get("person", "")))
        return keys

    def _conversation_keys(self, session_id: str, honcho_keys: dict[str, str],
                           lcm_conversation: Optional[str]) -> list[SourceKey]:
        agent = self._agent_name()
        chat = self._init.get("gateway_session_key", "")
        keys: list[SourceKey] = [
            ("gateway_chat", f"{agent}:{chat}" if chat else ""),
            ("lcm_conversation", f"{agent}:{lcm_conversation}" if lcm_conversation else ""),
            ("honcho_session", honcho_keys.get("conversation", "")),
        ]
        if not any(key for _, key in keys):
            keys.append(("hermes_session", f"{agent}:{session_id}"))
        return keys

    # ── memory.messages ──

    def _message_store(self):
        if self._store is None:
            from hermes_state_store import MessageStore, agent_id_for
            self._store = MessageStore(agent_id_for(self._state_db()))
        return self._store

    def _state_db(self) -> Path:
        if self._home:
            return Path(self._home) / "state.db"
        from hermes_constants import get_hermes_home
        return get_hermes_home() / "state.db"

    def _agent_name(self) -> str:
        from hermes_state_store import agent_id_for
        return agent_id_for(self._state_db())

    def _store_uid(self, message_uid: str) -> str:
        return self._message_store().base_uid(message_uid)

    def _fill_conversation(self, record: TurnRecord) -> None:
        if not (record.conversation_id and record.message_uids):
            return
        try:
            self._message_store().set_conversation(record.message_uids, record.conversation_id)
        except Exception as exc:  # health: allow BLE001 -- the store being down must not cost the engines the turn
            logger.warning("lina-memory could not fill conversation_id: %s", exc)

    # ── everything else is the Honcho adapter's ──

    def system_prompt_block(self) -> str:
        return self._honcho.system_prompt_block() if self._honcho_on else ""

    def session_card_block(self) -> str:
        return self._honcho.session_card_block() if self._honcho_on else ""

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        return self._honcho.prefetch(query, session_id=session_id) if self._honcho_on else ""

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        if self._honcho_on:
            self._honcho.queue_prefetch(query, session_id=session_id)

    def recall_status(self) -> Optional[RecallStatus]:
        return self._honcho.recall_status() if self._honcho_on else None

    def get_tool_schemas(self) -> list[dict[str, Any]]:
        return self._honcho.get_tool_schemas() if self._honcho.is_available() else []

    def handle_tool_call(self, tool_name: str, args: dict[str, Any], **kwargs) -> str:
        return self._honcho.handle_tool_call(tool_name, args, **kwargs)

    def on_turn_start(self, turn_number: int, message: str, **kwargs) -> None:
        if self._honcho_on:
            self._honcho.on_turn_start(turn_number, message, **kwargs)

    def identity_signature(self) -> dict[str, Any]:
        return self._honcho.identity_signature()

    def on_session_end(self, messages: list[dict[str, Any]]) -> None:
        self._drain()
        if self._honcho_on:
            self._honcho.on_session_end(messages)

    def on_session_switch(self, new_session_id: str, **kwargs) -> None:
        self._claim(new_session_id)
        if self._honcho_on:
            self._honcho.on_session_switch(new_session_id, **kwargs)

    def on_pre_compress(self, messages: list[dict[str, Any]]) -> str:
        return self._honcho.on_pre_compress(messages) if self._honcho_on else ""

    def on_memory_write(self, action: str, target: str, content: str,
                        metadata: Optional[dict[str, Any]] = None) -> None:
        if self._honcho_on:
            self._honcho.on_memory_write(action, target, content, metadata)

    def get_config_schema(self) -> list[dict[str, Any]]:
        return self._honcho.get_config_schema()

    def save_config(self, values: dict[str, Any], hermes_home: str) -> None:
        self._honcho.save_config(values, hermes_home)

    def post_setup(self, hermes_home: str, config: dict) -> None:
        """Honcho's setup wizard, putting lina-memory (the provider chosen) in the memory slot when it finishes."""
        self._honcho.post_setup(hermes_home, config, memory_provider=PROVIDER_NAME)

    def backup_paths(self) -> list[str]:
        return self._honcho.backup_paths()

    def shutdown(self) -> None:
        lcm.release_session(self._session_id, self._take_lcm_turn)
        self._drain()
        if self._worker is not None:
            self._worker.shutdown(wait=False)
            self._worker = None
        if self._honcho_on:
            self._honcho.shutdown()
        self._registry.close()
        if self._store is not None:
            self._store.close()


def register(ctx) -> None:
    ctx.register_memory_provider(LinaMemoryProvider())
