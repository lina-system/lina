"""The outbound lock (lina#638): ``outbound.allow`` in the active profile's ``config.yaml`` lists the
only destinations a message may leave to. Unset or an empty list means unrestricted; any other value
that names no usable destination (``false``, ``0``, ``{}``, a list of blanks) allows nothing.

Every egress door calls :func:`check_outbound` with the destination as that door will send it, i.e.
after alias / home-channel resolution, and refuses the send when it returns a string. Doors:
``send_message`` (tool, ``hermes send``, kanban notifier) through ``_send_to_platform`` and
``_handle_react``; every public adapter coroutine that takes a ``chat_id`` or ``parent_chat_id``,
inherited ones included, except the content-free ones in :data:`NOT_OUTBOUND` (wrapped by
:func:`guard_adapter_class` from ``BasePlatformAdapter.__init_subclass__``, so a new door is guarded
without being listed; a refused edit ends the stream like a connector decline, a refused task card
suppresses its text fallback); cron delivery (``cron/scheduler_delivery_outbound.py``) and the
parked Bot Chat records it drains later (``cron/bot_chat_legacy.py``);
``hermes peer dm``/``run``; ``message_agent``; the webhook ``github_comment`` deliver; worker end
notices. ``tests/agent/test_outbound_lock_doors.py`` drives each door with a fake transport.

Entries: ``<platform>:<chat id>[:<thread id>]``, parsed like ``resolve_send_target`` (an entry
without a thread allows every thread of the chat), and ``agent:<name>`` for another agent (a local
profile, a peer ``<peer>``, which allows all its agents, or ``<peer>/<agent>``). The config is read
through ``load_user_config_effective`` (the managed overlay applied), cached on the file signatures,
so an edit applies to the next send without a restart; resolved entries are cached per profile home and channel-directory state.

Whose list applies is decided in one place, :func:`_policy_home`: the active profile, except while an
adapter delivers a reply after the runner's handler returned (it left the routed profile's scope by
then). Those paths bind the inbound source with :func:`delivering_for`, and the routed profile pinned
on that source decides. Only the policy read moves; delivery bookkeeping stays in the launch home.

A refusal names ``outbound.allow``, is logged once per target per process, and is counted in
``<HERMES_HOME>/outbound_refusals.json``, which ``hermes doctor`` reports.
"""

from __future__ import annotations

import contextlib
import contextvars
import functools
import inspect
import json
import logging
import threading
import time
from pathlib import Path
from typing import Any, Optional

logger = logging.getLogger(__name__)

CONFIG_KEY = "outbound.allow"
AGENT_PLATFORM = "agent"
REFUSALS_FILE = "outbound_refusals.json"
# The parameters an adapter method names its destination with (``parent_chat_id``: a new thread under it).
TARGET_PARAMETERS = ("chat_id", "parent_chat_id")
# Public adapter coroutines with a destination that put no content in the chat: reads, presence signals
# (a refused typing tick would be counted every few seconds), removals and local interrupt control.
# Every other public coroutine with a destination is guarded; this list is the only way out.
NOT_OUTBOUND = frozenset({
    "get_chat_info", "wait_for_auto_thread_info", "send_typing", "stop_typing", "mark_read",
    "delete_message", "interrupt_session_activity", "on_interrupt", "stop_native_task_card_progress",
})

# Adapters whose own send* answers the client that asked (the local app/TUI stream, the API server's HTTP
# caller) or routes to a deliver target that is checked on its own (webhook: github_comment here, other
# platforms through that platform's guarded adapter): not a message leaving to a chat.
ANSWERING_PLATFORMS = frozenset({"local", "api_server", "webhook"})

# (inbound source, active home when bound) of the reply an adapter is delivering; see delivering_for.
_delivery: contextvars.ContextVar[Optional[tuple[Any, str]]] = contextvars.ContextVar(
    "outbound_delivery", default=None)
_logged: set[tuple[str, str]] = set()
_parsed: dict[tuple, tuple[tuple[str, str, Optional[str]], ...]] = {}
_counts_lock = threading.Lock()


def allow_entries() -> Optional[list[str]]:
    """The active profile's ``outbound.allow`` entries (managed overlay applied), or None when
    unrestricted: only an unset key or an empty list. Any other value that is not a string or a list,
    and a string or list with no non-blank entry, allows nothing (fails closed)."""
    from hermes_cli.config_effective import load_user_config_effective
    section = load_user_config_effective().get("outbound")
    if section is None:
        return None
    if not isinstance(section, dict):
        return []
    allow = section.get("allow")
    if allow is None or allow == []:
        return None
    if isinstance(allow, str):
        allow = [allow]
    if not isinstance(allow, list):
        return []
    return [str(entry).strip() for entry in allow if str(entry).strip()]


def _parse_entry(entry: str) -> Optional[tuple[str, str, Optional[str]]]:
    platform, sep, ref = entry.partition(":")
    platform, ref = platform.strip().lower(), ref.strip()
    if not sep or not platform or not ref:
        logger.warning("a %s entry is not '<platform>:<chat id>' or 'agent:<name>'; it allows nothing", CONFIG_KEY)
        return None
    if platform == AGENT_PLATFORM:
        return AGENT_PLATFORM, ref.lower(), None
    from tools.send_message_targets import resolve_send_target
    try:
        chat_id, thread_id, error = resolve_send_target(platform, ref, pass_unresolved_references=True)
    except Exception as exc:  # health: allow BLE001 -- plugin discovery can raise anything; logged with traceback
        logger.warning("a %s entry could not be parsed; matching it as written", CONFIG_KEY, exc_info=True)
        chat_id, thread_id, error = None, None, str(exc)
    if error or not chat_id:
        # An entry for a platform this process cannot load is kept as written.
        return platform, ref, None
    return platform, str(chat_id), str(thread_id) if thread_id else None


def _alias_signature() -> tuple:
    """What a channel-name entry resolves through: the active profile's channel directory and alias
    files, by stat (a rebuild or an alias edit changes it)."""
    from gateway.channel_directory import _aliases_path, _directory_path
    signature = []
    for path in (_directory_path(), _aliases_path()):
        try:
            stat = path.stat()
            signature.append((stat.st_mtime_ns, stat.st_size))
        except OSError:
            signature.append(None)
    return tuple(signature)


def _parsed_entries(entries: list[str]) -> tuple[tuple[str, str, Optional[str]], ...]:
    """Entries resolved for the active profile: one process serves many profiles, and the same entry
    text (``telegram:trial``) can name a different chat in each profile's channel directory."""
    from hermes_constants import get_hermes_home
    key = (str(get_hermes_home()), tuple(entries), _alias_signature())
    if key not in _parsed:
        if len(_parsed) > 256:
            _parsed.clear()
        _parsed[key] = tuple(p for p in map(_parse_entry, entries) if p is not None)
    return _parsed[key]


def _allowed(entries, platform: str, chat_id: str, thread_id: Optional[str]) -> bool:
    for entry_platform, entry_chat, entry_thread in entries:
        if entry_platform != platform:
            continue
        if platform == AGENT_PLATFORM:
            name = chat_id.lower()
            if name == entry_chat or name.startswith(entry_chat + "/"):
                return True
        elif entry_chat == chat_id and (entry_thread is None or entry_thread == thread_id):
            return True
    return False


def target_label(platform: str, chat_id: Any, thread_id: Any = None) -> str:
    label = f"{platform}:{chat_id}"
    return f"{label}:{thread_id}" if thread_id not in (None, "") else label


@contextlib.contextmanager
def delivering_for(source: Any):
    """Check sends made inside against the profile ``source`` was routed to (its pinned
    ``RoutingIdentity``), resolved at send time. Bound by every path that awaits the runner's message
    handler and then delivers its reply."""
    from hermes_constants import get_hermes_home
    token = _delivery.set((source, str(get_hermes_home())))
    try:
        yield
    finally:
        _delivery.reset(token)


def _policy_home() -> Optional[Path]:
    """The profile home whose list applies when it is not the active one: the delivered source's
    routed profile, unless a profile scope was entered since :func:`delivering_for` bound it (that
    scope is the more specific answer, e.g. the turn itself running inside the handler)."""
    from hermes_constants import get_hermes_home
    bound = _delivery.get()
    if bound is None or str(get_hermes_home()) != bound[1]:
        return None
    from gateway.session_identity import identity_of
    identity = identity_of(bound[0])
    return identity.runtime_home if identity is not None else None


def check_outbound(platform: Any, chat_id: Any, thread_id: Any = None, *, door: str) -> Optional[str]:
    """None when a message may leave to ``platform:chat_id[:thread_id]`` (``platform="agent"``:
    ``chat_id`` is the agent name), else the refusal text, which names ``outbound.allow``. ``door``
    names the caller in the log and the doctor count. Public: cron's dry run checks its delivery
    targets with it."""
    home = _policy_home()
    if home is None:
        return _check(platform, chat_id, thread_id, door)
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override
    token = set_hermes_home_override(str(home))
    try:
        return _check(platform, chat_id, thread_id, door)
    finally:
        reset_hermes_home_override(token)


def _check(platform: Any, chat_id: Any, thread_id: Any, door: str) -> Optional[str]:
    entries = allow_entries()
    if entries is None:
        return None
    platform = str(getattr(platform, "value", platform) or "").strip().lower()
    chat = "" if chat_id is None else str(chat_id).strip()
    thread = None if thread_id in (None, "") else str(thread_id).strip()
    if chat and _allowed(_parsed_entries(entries), platform, chat, thread):
        return None
    return _refuse(target_label(platform, chat or "(none)", thread), door)


def _refuse(target: str, door: str) -> str:
    from hermes_constants import display_hermes_home, get_hermes_home
    home = get_hermes_home()
    message = (f"Refused by {CONFIG_KEY}: {target} is not in the {CONFIG_KEY} list in "
               f"{display_hermes_home()}/config.yaml, so nothing was sent ({door}).")
    if (str(home), target) not in _logged:
        _logged.add((str(home), target))
        logger.warning("%s", message)
    try:
        _count(home, target, door)
    except OSError as exc:
        logger.warning("Could not record the %s refusal of %s in %s: %s", CONFIG_KEY, target, REFUSALS_FILE, exc)
    return message


def _count(home, target: str, door: str) -> None:
    from utils import atomic_json_write
    path = home / REFUSALS_FILE
    with _counts_lock:
        data = read_refusals(home)
        row = data["targets"].setdefault(target, {"count": 0})
        row.update(count=row["count"] + 1, door=door, last_at=time.time())
        data["total"] += 1
        atomic_json_write(path, data)


def read_refusals(home=None) -> dict:
    """``{"total": n, "targets": {target: {"count", "door", "last_at"}}}`` for a profile home."""
    if home is None:
        from hermes_constants import get_hermes_home
        home = get_hermes_home()
    try:
        data = json.loads((home / REFUSALS_FILE).read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return {"total": 0, "targets": {}}
    if not isinstance(data, dict) or not isinstance(data.get("targets"), dict):
        return {"total": 0, "targets": {}}
    return {"total": int(data.get("total") or 0), "targets": data["targets"]}


def is_outbound_refusal(result: Any) -> bool:
    """True for the SendResult a guarded adapter method returns on a refusal: terminal, never retried
    (``gateway.relay.egress.declined_send`` classifies it as a destination refusal)."""
    raw = getattr(result, "raw_response", None)
    return isinstance(raw, dict) and raw.get("outbound_refused") is True


def guard_adapter_class(cls: type, base: type) -> None:
    """Route every public coroutine of ``cls`` (its own, a mixin's or an inherited one) that takes a
    :data:`TARGET_PARAMETERS` destination through :func:`check_outbound`, unless it is in
    :data:`NOT_OUTBOUND`. A method defined on ``base`` or an adapter class is wrapped in place on that
    class, so ``type(adapter).send_clarify is BasePlatformAdapter.send_clarify`` (how the gateway
    detects a native override) still holds; a plain mixin's is wrapped on ``cls``. A refusal returns at
    the outermost wrapper, so a default that ends in a guarded ``send()`` is never counted twice."""
    for name in dir(cls):
        if name.startswith("_") or name in NOT_OUTBOUND:
            continue
        owner = next((klass for klass in cls.__mro__ if name in klass.__dict__), None)
        method = owner.__dict__[name] if owner is not None else None
        if (method is None or getattr(method, "_outbound_guarded", False)
                or not inspect.iscoroutinefunction(method)):
            continue
        target = next((p for p in TARGET_PARAMETERS if p in inspect.signature(method).parameters), None)
        if target is not None:
            setattr(owner if issubclass(owner, base) else cls, name, _guarded(method, name, target))


def _relay_platform(adapter, chat_id, metadata: dict) -> Optional[str]:
    """The platform a relay adapter (``Platform.RELAY``) sends to: the fronted one, never ``relay``."""
    chat_platform = getattr(adapter, "_chat_platform", None)
    return metadata.get("_relay_logical_platform") or (chat_platform(chat_id) if callable(chat_platform) else None)


def _effective_thread(adapter, target: str, bound: dict, metadata: dict) -> Optional[str]:
    """The thread the adapter will post to. ``parent_chat_id`` opens a new thread, so the content lands
    at chat level. An adapter that rewrites the thread before its API call (Slack drops a thread equal to
    ``reply_to`` when ``reply_in_thread`` is off) resolves it with ``_resolve_thread_ts``."""
    if target == "parent_chat_id":
        return None
    resolve = getattr(type(adapter), "_resolve_thread_ts", None)
    if callable(resolve):
        return resolve(adapter, bound.get("reply_to"), metadata or None)
    return metadata.get("thread_id")


def _refused_result(signature: inspect.Signature, refusal: str):
    """What a refused method returns, by its declared return type: a failed SendResult (also when
    undeclared, the send* shape), a failed receipt dict, False, or None (no thread, no handle)."""
    returns = str(signature.return_annotation)
    if signature.return_annotation is inspect.Signature.empty or "SendResult" in returns:
        from gateway.platforms.base import SendResult
        return SendResult(success=False, error=refusal, error_kind="forbidden",
                          raw_response={"outbound_refused": True})
    if "dict" in returns:
        return {"success": False, "error": refusal, "outbound_refused": True}
    return False if returns == "bool" else None


def _guarded(method, name: str, target: str):
    signature = inspect.signature(method)

    @functools.wraps(method)
    async def guarded(self, *args, **kwargs):
        bound = signature.bind_partial(self, *args, **kwargs).arguments
        metadata = bound.get("metadata") if isinstance(bound.get("metadata"), dict) else {}
        chat_id = bound.get(target)
        platform = (bound.get("logical_platform") or _relay_platform(self, chat_id, metadata)
                    or getattr(self, "platform", ""))
        if str(getattr(platform, "value", platform)).lower() in ANSWERING_PLATFORMS:
            return await method(self, *args, **kwargs)
        refusal = check_outbound(platform, chat_id, _effective_thread(self, target, bound, metadata),
                                 door=f"gateway {name}")
        if refusal:
            return _refused_result(signature, refusal)
        return await method(self, *args, **kwargs)

    guarded._outbound_guarded = True
    return guarded
