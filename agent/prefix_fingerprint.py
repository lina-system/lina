"""Opt-in prompt-prefix fingerprints, one row per model request (lina#25).

With ``memory.prefix_fingerprints: true`` each request appends one JSON line to
``<home>/state/prefix-fingerprints.jsonl``: the time, a keyed hash of the session id, and for each leading block of
the request (tools, a separate system field, then each message, in the order the provider caches them) a keyed hash
and its byte length. No text is stored. The key is a random per-home salt beside the file, so a short known message
cannot be recognized by hashing it. ``lina memory cache-report --fingerprints`` reads the file to measure the exact
common prefix of consecutive requests.
"""

from __future__ import annotations

import hashlib
import hmac
import json
import logging
import os
import secrets
from datetime import UTC, datetime
from typing import Any

logger = logging.getLogger(__name__)

FILE_NAME = "prefix-fingerprints.jsonl"
SALT_NAME = "prefix-fingerprints.salt"


def enabled() -> bool:
    from hermes_cli.config import load_config_readonly

    memory = load_config_readonly().get("memory")
    return isinstance(memory, dict) and memory.get("prefix_fingerprints") is True


def request_blocks(api_kwargs: dict) -> list[bytes]:
    blocks = [json.dumps(api_kwargs[key], sort_keys=True, ensure_ascii=False, default=str).encode()
              for key in ("tools", "system") if api_kwargs.get(key) is not None]
    messages = api_kwargs.get("messages")
    if not isinstance(messages, list):
        messages = api_kwargs.get("input")
    if isinstance(messages, list):
        blocks += [json.dumps(m, sort_keys=True, ensure_ascii=False, default=str).encode() for m in messages]
    return blocks


def _salt(state) -> bytes:
    path = state / SALT_NAME
    try:
        fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    except FileExistsError:
        return path.read_bytes()
    salt = secrets.token_bytes(32)
    with os.fdopen(fd, "wb") as handle:
        handle.write(salt)
    return salt


def fingerprint(api_kwargs: dict, session_id: str, salt: bytes) -> dict[str, Any]:
    def digest(data: bytes) -> str:
        return hmac.new(salt, data, hashlib.sha256).hexdigest()[:24]

    return {"ts": datetime.now(UTC).isoformat(), "session": digest(session_id.encode()),
            "blocks": [[digest(block), len(block)] for block in request_blocks(api_kwargs)]}


def record(api_kwargs: dict, session_id: str) -> None:
    from hermes_constants import get_hermes_home

    state = get_hermes_home() / "state"
    state.mkdir(mode=0o700, parents=True, exist_ok=True)
    line = json.dumps(fingerprint(api_kwargs, session_id, _salt(state))) + "\n"
    fd = os.open(state / FILE_NAME, os.O_WRONLY | os.O_CREAT | os.O_APPEND, 0o600)
    with os.fdopen(fd, "a", encoding="utf-8") as handle:
        handle.write(line)


def capture(api_kwargs: dict, session_id: str) -> None:
    """Record the request when the home opted in; a failure is logged and never stops the request."""
    try:
        if enabled():
            record(api_kwargs, session_id)
    except Exception:
        logger.warning("prefix fingerprint capture failed; the request goes out unrecorded", exc_info=True)
