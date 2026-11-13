"""The outbound lock (lina#638): ``outbound.allow`` in the active profile's ``config.yaml`` lists the
only destinations a message may leave to. Unset or an empty list means unrestricted; any other value
that names no usable destination (``false``, ``0``, ``{}``, a list of blanks) allows nothing.

Every egress door calls :func:`check_outbound` with the destination as that door will send it, i.e.
after alias / home-channel resolution, and refuses the send when it returns a string. Doors:
``send_message`` (tool, ``hermes send``, kanban notifier) through ``_send_to_platform`` and
``_handle_react``; every gateway adapter content method in :data:`ADAPTER_MESSAGE_METHODS`, inherited
ones included (wrapped by :func:`guard_adapter_class` from ``BasePlatformAdapter.__init_subclass__``;
a refused edit ends the stream like a connector decline, a refused task card suppresses its text
fallback); cron delivery (``cron/scheduler_delivery_outbound.py``);
``hermes peer dm``/``run``; ``message_agent``; the webhook ``github_comment`` deliver; worker end
notices. ``tests/agent/test_outbound_lock_doors.py`` drives each door with a fake transport.

Entries: ``<platform>:<chat id>[:<thread id>]``, parsed like ``resolve_send_target`` (an entry
without a thread allows every thread of the chat), and ``agent:<name>`` for another agent (a local
profile, a peer ``<peer>``, which allows all its agents, or ``<peer>/<agent>``). The config is read
through ``load_user_config_effective`` (the managed overlay applied), cached on the file signatures,
so an edit applies to the next send without a restart; resolved entries are cached per profile home and channel-directory state.

A refusal names ``outbound.allow``, is logged once per target per process, and is counted in
``<HERMES_HOME>/outbound_refusals.json``, which ``hermes doctor`` reports.
"""

from __future__ import annotations

import functools
import inspect
import json
import logging
import threading
import time
from typing import Any, Optional

logger = logging.getLogger(__name__)

CONFIG_KEY = "outbound.allow"
AGENT_PLATFORM = "agent"
REFUSALS_FILE = "outbound_refusals.json"
# The adapter methods that put content in a chat (a send, an edit carrying new text, a prompt, a
# picker, a task card, played audio, a draft seal); each takes the destination as ``chat_id``.
ADAPTER_MESSAGE_METHODS = (
    "send", "send_for_platform", "send_draft", "edit_message", "send_exec_approval", "send_slash_confirm",
    "send_clarify", "send_private_notice", "send_multiple_images", "send_image", "send_animation",
    "send_voice", "send_video", "send_document", "send_image_file", "send_native_task_card_progress",
    "send_update_prompt", "send_model_picker", "send_choice_picker", "play_tts", "abandon_open_draft",
)

# Adapters whose own send* answers the client that asked (the local app/TUI stream, the API server's HTTP
# caller) or routes to a deliver target that is checked on its own (webhook: github_comment here, other
# platforms through that platform's guarded adapter): not a message leaving to a chat.
ANSWERING_PLATFORMS = frozenset({"local", "api_server", "webhook"})

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
        logger.warning("%s entry %r is not '<platform>:<chat id>' or 'agent:<name>'; it allows nothing",
                       CONFIG_KEY, entry)
        return None
    if platform == AGENT_PLATFORM:
        return AGENT_PLATFORM, ref.lower(), None
    from tools.send_message_targets import resolve_send_target
    try:
        chat_id, thread_id, error = resolve_send_target(platform, ref, pass_unresolved_references=True)
    except Exception as exc:  # health: allow BLE001 -- plugin discovery can raise anything; logged with traceback
        logger.warning("%s entry %r could not be parsed; matching it as written", CONFIG_KEY, entry, exc_info=True)
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


def check_outbound(platform: Any, chat_id: Any, thread_id: Any = None, *, door: str) -> Optional[str]:
    """None when a message may leave to ``platform:chat_id[:thread_id]`` (``platform="agent"``:
    ``chat_id`` is the agent name), else the refusal text, which names ``outbound.allow``. ``door``
    names the caller in the log and the doctor count. Public: cron's dry run checks its delivery
    targets with it."""
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


def guard_adapter_class(cls: type) -> None:
    """Route the content methods ``cls`` defines, takes from a mixin or inherits from
    ``BasePlatformAdapter`` through :func:`check_outbound`. Inherited defaults are guarded too: some
    end in a guarded ``send()``, but ``send_exec_approval`` renders through
    ``_send_exec_approval_prompt``, which never passes one. A refusal returns at the outermost wrapper,
    so a default that does reach ``send()`` is never counted twice."""
    for name in ADAPTER_MESSAGE_METHODS:
        owner = next((klass for klass in cls.__mro__ if name in klass.__dict__), None)
        if owner is None:
            continue
        method = owner.__dict__[name]
        if (getattr(method, "_outbound_guarded", False)
                or not inspect.iscoroutinefunction(method)
                or "chat_id" not in inspect.signature(method).parameters):
            continue
        setattr(cls, name, _guarded(method, name))


def _relay_platform(adapter, chat_id, metadata: dict) -> Optional[str]:
    """The platform a relay adapter (``Platform.RELAY``) sends to: the fronted one, never ``relay``."""
    chat_platform = getattr(adapter, "_chat_platform", None)
    return metadata.get("_relay_logical_platform") or (chat_platform(chat_id) if callable(chat_platform) else None)


def _guarded(method, name: str):
    signature = inspect.signature(method)

    @functools.wraps(method)
    async def guarded(self, *args, **kwargs):
        bound = signature.bind_partial(self, *args, **kwargs).arguments
        metadata = bound.get("metadata") if isinstance(bound.get("metadata"), dict) else {}
        platform = (bound.get("logical_platform") or _relay_platform(self, bound.get("chat_id"), metadata)
                    or getattr(self, "platform", ""))
        if str(getattr(platform, "value", platform)).lower() in ANSWERING_PLATFORMS:
            return await method(self, *args, **kwargs)
        refusal = check_outbound(platform, bound.get("chat_id"), metadata.get("thread_id"),
                                 door=f"gateway {name}")
        if refusal:
            from gateway.platforms.base import SendResult
            return SendResult(success=False, error=refusal, error_kind="forbidden",
                              raw_response={"outbound_refused": True})
        return await method(self, *args, **kwargs)

    guarded._outbound_guarded = True
    return guarded
