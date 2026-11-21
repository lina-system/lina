"""Read the collector's append-only spool into the shared memory store (lina#18)."""

from __future__ import annotations

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path
from typing import BinaryIO

from hermes_state_store import ExternalBatch, ExternalMessage, ingest_external_batch
from hermes_cli.sources.spool import message_uid

_BATCH_LINES = 100
_NATIVE_FIELDS = ("account", "room_id", "room_name", "native_id", "author_id", "author_name",
                  "reply_to_uid", "thread_root_uid", "edited_ts", "deleted", "ingest_path", "attachments")


class SpoolError(ValueError):
    """The spool is malformed or no longer has its committed prefix."""


@dataclass(frozen=True)
class IngestResult:
    lines: int = 0
    inserted: int = 0
    offset: int = 0


def _settings() -> dict:
    from hermes_cli.config_effective import load_user_config_effective
    from hermes_constants import get_default_hermes_root

    config = load_user_config_effective(get_default_hermes_root() / "config.yaml", fail_closed=True)
    return config.get("memory", {}).get("external_messages", {})


def enabled() -> bool:
    """Read the host-wide source switch on every operation, including existing sessions."""
    return _settings().get("enabled", True) is True


def _source(config: dict, kind: str) -> dict:
    sources = config.get("sources")
    settings = sources.get(kind) if isinstance(sources, dict) else None
    return settings if isinstance(settings, dict) else {}


def source_settings(kind: str) -> dict:
    config = _settings()
    settings = _source(config, kind)
    return {**settings, "enabled": config.get("enabled", True) is True and settings.get("enabled") is True}


def disabled_sources() -> tuple[str, ...]:
    """Read opt-ins together so a single query uses one configuration snapshot."""
    config = _settings()
    return tuple(kind for kind in ("mail", "slack") if _source(config, kind).get("enabled") is not True)


def configured_spool() -> Path | None:
    """The host's root config owns this job, not the currently bound profile."""
    config = _settings()
    if config.get("enabled", True) is not True:
        return None
    value = config.get("spool", "")
    if not isinstance(value, str):
        raise SpoolError("memory.external_messages.spool must be a path string")
    return Path(value).expanduser() if value.strip() else None


def _masked(value):
    """Mask metadata too: attachment captions and URLs can contain credentials."""
    from agent.redact import redact_sensitive_text

    if isinstance(value, str):
        return redact_sensitive_text(value, force=True, redact_url_credentials=True).replace("\x00", "")
    if isinstance(value, list):
        return [_masked(item) for item in value]
    if isinstance(value, dict):
        return {_masked(key): _masked(item) for key, item in value.items() if key != "raw"}
    return value


def _message(message: dict, event_id: str) -> ExternalMessage:
    """Validate identity at the spool boundary, discard raw, then digest sanitized fields."""
    required = ("source", "room_id", "native_id", "uid", "author_id", "text", "ts", "ingest_path")
    if any(not isinstance(message.get(key), str) for key in required):
        raise SpoolError("message requires string identity, text, timestamp and ingest_path fields")
    source, room, native_id = (message[key] for key in ("source", "room_id", "native_id"))
    if message["uid"] != message_uid(source, room, native_id):
        raise SpoolError("message uid does not match its platform, room and native id")
    if source not in {"kakao", "discord", "line", "imessage", "mail", "slack"}:
        raise SpoolError("unsupported message platform")
    if message["ingest_path"] not in {"archive", "live", "export"}:
        raise SpoolError("unsupported message ingest_path")
    timestamp = datetime.fromisoformat(message["ts"])
    if timestamp.utcoffset() is None:
        raise SpoolError("message timestamp must include a timezone")
    native = _masked({key: message.get(key) for key in _NATIVE_FIELDS})
    link_field = {"mail": "display_url", "slack": "permalink"}.get(source)
    if link_field and isinstance(message.get(link_field), str):
        native[link_field] = _masked(message[link_field])
    native["platform"] = source
    content = _masked(message["text"])
    author = _masked(f"{source}:{message['author_id']}")
    channel = _masked(f"messages:{source}:{room}")
    ts = timestamp.isoformat()
    payload = json.dumps([content, author, channel, ts, native], sort_keys=True, ensure_ascii=False,
                         separators=(",", ":"))
    digest = hashlib.sha256(payload.encode()).hexdigest()
    native.update(event_id=event_id, payload_sha256=digest)
    return ExternalMessage(f"messages:{message['uid']}", content, author, channel, ts,
                           json.dumps(native, ensure_ascii=False), digest, message["ingest_path"])


def _event(line: bytes, offset: int) -> tuple[ExternalMessage, ...]:
    try:
        event = json.loads(line)
        if not isinstance(event, dict) or event.get("version") != 1:
            raise SpoolError("unsupported event version")
        event_id = event.get("event_id")
        if not isinstance(event_id, str) or re.fullmatch(r"[0-9a-fA-F]{32}", event_id) is None:
            raise SpoolError("event_id must be 32 hex characters")
        messages = event.get("messages")
        if not isinstance(messages, list) or any(not isinstance(message, dict) for message in messages):
            raise SpoolError("event messages must be an array of objects")
        return tuple(_message(message, event_id) for message in messages)
    except (ValueError, TypeError, UnicodeError) as exc:
        # Never include the input or parser exception: either could contain the unmasked text.
        raise SpoolError(f"invalid spool event at byte {offset} ({type(exc).__name__})") from None


class _SpoolReader:
    """Keep the incremental hash between batches; revalidate from zero after another reader advances."""

    def __init__(self, stream: BinaryIO) -> None:
        self.stream = stream
        self.offset = 0
        self.digest = hashlib.sha256()

    def read_batch(self, offset: int, expected: str) -> ExternalBatch:
        if self.offset != offset or self.digest.hexdigest() != expected:
            self.stream.seek(0)
            self.digest = hashlib.sha256()
            remaining = offset
            while remaining:
                chunk = self.stream.read(min(remaining, 1024 * 1024))
                if not chunk:
                    raise SpoolError("spool was truncated before the committed cursor; ingestion stopped")
                self.digest.update(chunk)
                remaining -= len(chunk)
        if self.digest.hexdigest() != expected:
            raise SpoolError("spool consumed prefix was rewritten; ingestion stopped")
        self.stream.seek(offset)
        messages, lines = [], 0
        for _ in range(_BATCH_LINES):
            line = self.stream.readline()
            if not line.endswith(b"\n"):
                break
            messages.extend(_event(line, offset))
            self.digest.update(line)
            offset += len(line)
            lines += 1
        self.offset = offset
        return ExternalBatch(tuple(messages), lines, offset, self.digest.hexdigest())


def ingest(spool: Path | None = None, *, cursor_key: str | None = None) -> IngestResult:
    """Consume complete lines under the collector's shared lock; never write beside the spool."""
    if not enabled():
        return IngestResult()
    path = spool if spool is not None else configured_spool()
    if path is None:
        return IngestResult()
    import fcntl

    path = path.resolve()
    lines, inserted, offset = 0, 0, 0
    with path.open("rb") as stream:
        fcntl.flock(stream.fileno(), fcntl.LOCK_SH)
        reader = _SpoolReader(stream)
        while True:
            batch, added = ingest_external_batch(cursor_key or str(path), reader.read_batch)
            lines += batch.lines
            inserted += added
            offset = batch.offset
            if batch.lines < _BATCH_LINES:
                return IngestResult(lines, inserted, offset)


def ingest_command(args) -> None:
    """CLI boundary: failures must not echo unmasked spool data or database payloads."""
    import psycopg
    from hermes_state_store import StoreUnavailableError

    try:
        path = Path(args.spool).expanduser() if args.spool is not None else configured_spool()
        if path is None:
            print("External messages: no spool configured; nothing to ingest.")
            return
        result = ingest(path)
    except (OSError, ValueError, psycopg.Error, StoreUnavailableError) as exc:
        detail = str(exc) if isinstance(exc, SpoolError) else type(exc).__name__
        print(f"External messages ingestion stopped: {detail}")
        raise SystemExit(1) from None
    print(f"External messages: {result.lines} lines, {result.inserted} rows inserted, byte offset {result.offset}.")
