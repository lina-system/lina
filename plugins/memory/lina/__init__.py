"""lina-memory: the memory layer in the core's memory-provider slot (``memory.provider: lina-memory``, lina#20).

Each finished turn of its agent is received once and routed by the policy table (``policy.yaml``). LCM-X gets it
where LCM-X's own ``post_llm_call`` hook fires: lina-memory claims its session (``lcm.claim_session``), and the
hook hands the turn to ``_take_lcm_turn`` instead of ingesting it itself (``lcm.py``). ``sync_turn``, called once
per finished turn by ``MemoryManager.sync_all``, builds one turn record (person, agent and conversation ids from
the identity registry, the turn's message uids, the route it arrived on, platform and timestamps) and hands it to
the Honcho adapter (``plugins/memory/honcho``, which keeps its code and is driven only from here).
Every other provider call (recall, tools, session lifecycle) goes to the Honcho adapter when Honcho is configured,
except ``memory_search`` and ``memory_open`` (``search.py``), lina-memory's own tools over every source (lina#22),
``memory_link`` (``links.py``), ``owner_terms`` (``terms.py``, the owner's own words for a concept, lina#379) and
``mark_personal`` (``personal.py``, lina#308: a personal document of the job; its ``pre_tool_call`` hook marks
documents from marker folders and refuses public posts and third-party uploads of them; the job's work folders are
deleted at session end).
The person card comes from the session person's brain page, and Honcho's conclusions about that person are written
to the page at session end (``hermes_cli/brain/people.py``, lina#445).

``brain_search``, ``brain_get`` and ``brain_links`` (``search.py``, lina#446) are the one search over the brain, the
work record, repos and memory; ``memory_search`` and ``memory_open`` forward to them for one release.
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import Any, Optional

from agent.memory_provider import MemoryProvider, RecallStatus, ctx_bound

from . import grade, lcm, links, personal, search, speaker, terms
from .identity import OWNER_HANDLE, IdentityRegistry, SourceKey
from .policy import load_policy, signals_for_turn, turn_start_index
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
        self._search: Optional[search.MemorySearch] = None
        self._embedding = None
        self._brain = None

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
        # A snapshot: the next turn appends to the transcript while this one is delivered.
        history = list(local_messages if local_messages is not None else messages or ())
        author = dict(turn_author) if isinstance(turn_author, dict) else {}
        honcho_session = self._honcho.session_key if self._honcho_on else ""
        rule = self._policy.rule(self._policy.route_for(signals_for_turn(
            history, agent_context=self._init.get("agent_context", ""), platform=self._init.get("platform", ""),
            turn_author=author or None, turn_start=turn_start)))
        lcm_conversation = lcm.conversation_id(session_id)

        # The owner's words and the gateway's notes, from the egress-redacted copy Honcho may have.
        sent = list(messages or ())
        sent_index = turn_start_index(sent, turn_start)
        notes = speaker.notes_of(sent[sent_index]) if sent_index is not None else None

        def deliver() -> None:
            record = self._record(rule, history, session_id=session_id, author=author, turn_start=turn_start,
                                  honcho_session=honcho_session, lcm_conversation=lcm_conversation,
                                  gateway_notes=tuple((notes or {}).get("parts") or ()))
            if record.honcho and self._honcho_on:
                self._honcho.sync_turn(notes["owner_text"] if notes else user_content, assistant_content,
                                       session_id=session_id, turn_author=author, messages=messages,
                                       turn_record=record, session_key=honcho_session)
            self._fill_conversation(record)
            self._fill_speakers(record)

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
                honcho_session: str, lcm_conversation: Optional[str], gateway_notes: tuple = ()) -> TurnRecord:
        honcho_keys = self._honcho.source_keys(author, session_key=honcho_session) if self._honcho_on else {}
        agent = self._agent_name()
        person_id = None if author.get("is_bot") else self._registry.resolve(
            "person", self._person_keys(author, honcho_keys))
        agent_keys: list[SourceKey] = [("profile", agent), ("honcho_peer", honcho_keys.get("agent", ""))]
        handle = self._author_handle(author)
        speakers = speaker.turn_speakers(
            self._policy, history, turn_start, agent_context=self._init.get("agent_context", ""),
            platform=self._init.get("platform", ""), author=author, agent_ref=f"agent:{agent}",
            person_ref=links.row_refs([handle])[0] if handle else None,
            owner_handle=lambda: bool(handle) and self._registry.person_of(handle) == self._registry.person_of(
                OWNER_HANDLE))
        return build_record(
            rule, turn_rows(history, turn_start), store_uid=self._store_uid, person_id=person_id,
            agent_id=self._registry.resolve("agent", agent_keys) or f"agent:{agent}",
            conversation_id=self._registry.resolve("conversation", self._conversation_keys(
                session_id, honcho_keys, lcm_conversation)) or "", session_id=session_id, platform=self._init.get("platform", ""),
            author_id=str(author.get("id") or "") or None, speakers=speakers, gateway_notes=gateway_notes,
        )

    def _author_handle(self, author: dict[str, Any]) -> Optional[SourceKey]:
        """The turn author's handle: their gateway user, or the machine owner on a local surface."""
        platform = self._init.get("platform", "")
        user_id = str(author.get("id") or "") or self._init.get("user_id", "")
        if user_id:
            return ("gateway_user", f"{platform}:{user_id}")
        return OWNER_HANDLE if platform in _LOCAL_PLATFORMS else None

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
            # The human session, when a bot turn's conversation key is its a2a session instead.
            ("honcho_session", honcho_keys.get("base_conversation", "")),
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

    def _fill_speakers(self, record: TurnRecord) -> None:
        if not record.speakers:
            return
        try:
            self._message_store().set_speakers(record.speakers)
        except Exception as exc:  # health: allow BLE001 -- the store being down must not cost the engines the turn
            logger.warning("lina-memory could not fill the speakers of a turn: %s", exc)

    # ── memory_search / memory_open / memory_link ──

    def _memory_search(self) -> search.MemorySearch:
        if self._search is None:
            self._search = search.MemorySearch(search.Readers(
                store=self._message_store, honcho=self._honcho_reader, lcm=self._use_lcm, embedder=self._embedder,
                person_refs=self._person_refs, brain=self._brain_reader, work=self._work_items))
        return self._search

    def _brain_reader(self):
        if self._brain is None:
            from hermes_cli.brain.search import BrainReader
            self._brain = BrainReader(self._brain_connection)
        return self._brain

    def _brain_connection(self):
        """A read connection as this agent (role ``lina_app``): it may read the shared brain and page vectors."""
        import psycopg

        from hermes_state_store import StoreUnavailableError, resolve_dsn
        try:
            conn = psycopg.connect(resolve_dsn(), autocommit=True)
        except psycopg.OperationalError as exc:
            raise StoreUnavailableError(f"cannot reach the memory store: {exc}".strip()) from exc
        conn.execute("SELECT set_config('lina.agent_id', %s, false)", (self._agent_name(),))
        return conn

    @staticmethod
    def _work_items() -> dict[str, dict[str, Any]]:
        """The work record's items, on a connection of their own: the ledger's per-context one would outlive the
        tool call."""
        from hermes_cli.work import ledger, store
        db = ledger.open_db()
        try:
            return store.read_all(db)[0]
        finally:
            db.close()

    def _person_refs(self, person: str) -> list[str]:
        """The row refs of every handle of *person* (a person id or a handle): its confirmed channels only."""
        return links.row_refs(self._registry.handles(links.person_id(self._registry, person)))

    def _honcho_reader(self):
        if not self._honcho_on:
            raise search.SourceOff("Honcho is not configured for this agent")
        if not self._honcho._ensure_session():
            raise search.SourceOff("the Honcho session is not ready")
        return self._honcho._manager, self._honcho.session_key

    def _use_lcm(self, operation):
        used, value, why_not = lcm.use_engine(self._session_id, operation)
        if not used:
            raise search.SourceOff(why_not)
        return value

    def _embedder(self):
        """The embedding client of ``embedding:`` in config.yaml, or None when no endpoint is configured. It does not
        retry a rate-limited answer: a search inside a turn fails at once rather than wait out a backoff."""
        if self._embedding is None:
            from dataclasses import replace

            from agent.embedding_client import EmbeddingClient, EmbeddingError, EmbeddingSettings
            try:
                self._embedding = EmbeddingClient(replace(EmbeddingSettings.from_config(), max_retries=0))
            except EmbeddingError:
                self._embedding = False
        return self._embedding or None

    # ── everything else is the Honcho adapter's ──

    def system_prompt_block(self) -> str:
        return self._honcho.system_prompt_block() if self._honcho_on else ""

    def session_card_block(self) -> str:
        """The person card from the session person's brain page (lina#445), with Honcho's session summary and the
        agent's own sections; Honcho's card of the person only while the person has no page."""
        page_card = self._page_card()
        if page_card is None:
            return self._honcho.session_card_block() if self._honcho_on else ""
        rest = self._honcho.session_card_block(person=False) if self._honcho_on else ""
        return "\n\n".join(part for part in (page_card, rest) if part)

    def _session_person(self) -> Optional[str]:
        """The person this session is with: the one its author's handle is confirmed as, else the handle's own."""
        handle = self._author_handle({})
        return self._registry.person_of(handle) if handle else None

    def _page_card(self) -> Optional[str]:
        try:
            person = self._session_person()
            if not person:
                return None
            import psycopg

            import hermes_state_store
            from hermes_cli.brain import people
            with psycopg.connect(hermes_state_store.resolve_dsn()) as conn:
                return people.card_for(conn, person)
        except Exception as exc:  # health: allow BLE001 -- the store being down must not cost the prompt its card
            logger.warning("lina-memory could not read the person's brain page; using Honcho's card: %s", exc)
            return None

    def _pages_from_honcho(self) -> None:
        """Honcho's conclusions about the session's person, as ``agent`` entries on their brain page (lina#445)."""
        try:
            person = self._session_person()
            found = self._honcho.person_conclusions() if person else []
            if not found:
                return
            from hermes_cli.brain import people
            from hermes_cli.brain.store import owner_connection
            with owner_connection() as conn:
                added = people.record_conclusions(conn, person, [people.conclusion_from(raw) for raw in found])
            if added:
                logger.info("lina-memory wrote %d Honcho conclusions to the person's brain page", added)
        except Exception as exc:  # health: allow BLE001 -- a page write failing must not hold session end
            logger.warning("lina-memory could not write Honcho's conclusions to the person's brain page: %s", exc)

    def prefetch(self, query: str, *, session_id: str = "") -> str:
        honcho = self._honcho.prefetch(query, session_id=session_id) if self._honcho_on else ""
        return self._with_external_messages(honcho, query)

    def recall_query(self, query: str, *, session_id: str = "") -> str:
        """Explicit ``memory_recall``. Honcho's prefetch is cadence-gated; this is not."""
        honcho = self._honcho.recall_query(query, session_id=session_id) if self._honcho_on else ""
        return self._with_external_messages(honcho, query)

    def _with_external_messages(self, honcho: str, query: str) -> str:
        """Per-turn and explicit recall both carry external-message hits; the source switch empties them (lina#23).
        They enter the prompt through ``grade.recall_text``: an external hit only inside its data block (lina#440)."""
        found = self._memory_search().search(query, sources=["messages"])
        messages = grade.recall_text(found["results"])
        return "\n\n".join(part for part in (honcho, messages) if part)

    def queue_prefetch(self, query: str, *, session_id: str = "") -> None:
        if self._honcho_on:
            self._honcho.queue_prefetch(query, session_id=session_id)

    def recall_status(self) -> Optional[RecallStatus]:
        return self._honcho.recall_status() if self._honcho_on else None

    def get_tool_schemas(self) -> list[dict[str, Any]]:
        honcho = self._honcho.get_tool_schemas() if self._honcho.is_available() else []
        return [*honcho, *search.SCHEMAS, links.LINK_SCHEMA, terms.TERMS_SCHEMA, personal.MARK_SCHEMA]

    def handle_tool_call(self, tool_name: str, args: dict[str, Any], **kwargs) -> str:
        if tool_name in search.TOOL_NAMES:
            return search.handle_tool_call(self._memory_search(), tool_name, args)
        if tool_name == links.LINK_TOOL:
            return links.handle_tool_call(self._registry, args)
        if tool_name == terms.TERMS_TOOL:
            return terms.handle_tool_call(self._message_store(), args)
        if tool_name == personal.MARK_TOOL:
            return personal.handle_mark(args, self._session_id, str(kwargs.get("task_id") or ""))
        return self._honcho.handle_tool_call(tool_name, args, **kwargs)

    def on_turn_start(self, turn_number: int, message: str, **kwargs) -> None:
        if self._honcho_on:
            self._honcho.on_turn_start(turn_number, message, **kwargs)

    def identity_signature(self) -> dict[str, Any]:
        return self._honcho.identity_signature()

    def on_session_end(self, messages: list[dict[str, Any]]) -> None:
        self._drain()
        self._end_personal_job()
        if self._honcho_on:
            self._honcho.on_session_end(messages)
            self._submit(self._pages_from_honcho)
            self._drain()

    def _end_personal_job(self) -> None:
        """The session's job is over: its work folders on this device are deleted (lina#308)."""
        if not self._session_id:
            return
        from agent import personal_work
        try:
            marks = personal_work.marks_for_home()
            personal_work.end_job(marks, marks.job_of(self._session_id), "session_end")
        except Exception as exc:  # health: allow BLE001 -- session end goes on; the folders stay for the next end
            logger.warning("lina-memory could not end the personal job of session %s: %s", self._session_id, exc)

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
        if self._embedding:
            self._embedding.close()
            self._embedding = None
        if self._brain is not None:
            self._brain.close()
            self._brain = None
        if self._store is not None:
            self._store.close()


def register(ctx) -> None:
    ctx.register_memory_provider(LinaMemoryProvider())
    ctx.register_hook("pre_tool_call", personal.pre_tool_call)
