"""Read-only provider boundary for the manual message collector."""

from __future__ import annotations

import hashlib
import json
import os
import re
import subprocess
import uuid
from datetime import UTC, datetime
from urllib.parse import urlsplit


TOOLS = {
    "mail": frozenset({"GMAIL_FETCH_EMAILS", "GMAIL_FETCH_MESSAGE_BY_MESSAGE_ID"}),
    "slack": frozenset({
        "SLACK_LIST_ALL_CHANNELS", "SLACK_FETCH_CONVERSATION_HISTORY", "SLACK_FIND_USERS",
        "SLACK_FETCH_MESSAGE_THREAD_FROM_A_CONVERSATION", "SLACK_RETRIEVE_MESSAGE_PERMALINK_URL",
    }),
}
MESSAGE_LIMIT = 100
TEXT_LIMIT = 4000


class SourceError(Exception):
    """A fixed reason suitable for display and persistence, without provider diagnostics."""

    def __init__(self, kind: str, *, auth: bool = False):
        self.kind = kind
        self.auth = auth
        super().__init__(kind)


def check_tool(kind, tool):
    if not isinstance(tool, str) or tool not in TOOLS.get(kind, ()):
        raise SourceError("tool_denied")


_AUTH = re.compile(
    r"\b(?:401|403|unauthenticated|unauthorized|forbidden|invalid[_ -]auth|not[_ -]authed|"
    r"invalid[_ -](?:grant|token)|token[_ -](?:expired|revoked)|missing[_ -]scope|"
    r"no[_ -]token|connection[_ -](?:required|expired)|authentication[_ -]required|"
    r"account[_ -]inactive|inactive[_ -]account|(?:connected[_ -])?account.{0,50}not (?:in )?(?:an? )?active|"
    r"no (?:matching )?connected account|non-active|account.{0,32}(?:inactive|expired|revoked)|"
    r"not logged in)\b", re.IGNORECASE,
)
_RATE = re.compile(r"\b(?:429|ratelimited|(?:provider_)?rate[_ -]limit(?:ed)?|too many requests)\b", re.IGNORECASE)


def failure(detail, status=None):
    """Inspect only a failed operation. Provider strings must never escape this boundary."""
    text = str(detail)
    if status == 429:
        return SourceError("rate_limited")
    if status in (401, 403) or _AUTH.search(text):
        return SourceError("auth", auth=True)
    if _RATE.search(text):
        return SourceError("rate_limited")
    return SourceError("provider_error")


def payload(value):
    """Unwrap the gateway/CLI and toolkit envelopes, including JSON-encoded action data."""
    for _ in range(12):
        if isinstance(value, str):
            try:
                value = json.loads(value)
            except ValueError:
                break
        if not isinstance(value, dict):
            break
        account = value.get("connected_account") or value.get("connectedAccount")
        account_status = value.get("account_status") or value.get("connected_account_status")
        if isinstance(account, dict):
            account_status = account.get("status") or account_status
        if isinstance(account_status, str) and account_status.upper() != "ACTIVE":
            raise SourceError("inactive_account", auth=True)
        if value.get("error") or any(value.get(key) is False for key in ("ok", "success", "successful")):
            raise failure(value.get("error") or value)
        for key in ("result", "data", "response"):
            if key in value:
                value = value[key]
                break
        else:
            return value
    raise SourceError("invalid_response")


def rows(page, key):
    items = page.get(key)
    if not isinstance(items, list) or any(not isinstance(item, dict) for item in items):
        raise SourceError("invalid_response")
    return items


def paged_rows(call, tool, params, account, window, field, identity, *, gmail=False, thread=False):
    """Advance after durable emission. A changed page order invalidates its saved offset."""
    position = window.position
    token_key, offset_key, hash_key = (
        ("thread_page_token", "thread_offset", "thread_page_hash") if thread
        else ("page_token", "offset", "page_hash")
    )
    cursor = position.get(token_key)
    seen = {cursor}
    while True:
        args = dict(params)
        if cursor:
            args["page_token" if gmail else "cursor"] = cursor
        page = payload(call(tool, args, account))
        items = rows(page, field)
        ids = [identity(item) for item in items]
        if any(not isinstance(native, str) or not native for native in ids):
            raise SourceError("invalid_response")
        digest = hashlib.sha256(json.dumps(ids, separators=(",", ":")).encode()).hexdigest()
        offset = position.get(offset_key, 0) if digest == position.get(hash_key) else 0
        position[hash_key] = digest
        for index in range(offset, len(items)):
            yield items[index]
            position[offset_key] = index + 1
            window.save()
        if gmail:
            cursor = page.get("nextPageToken") or page.get("next_page_token")
        else:
            metadata = page.get("response_metadata") or {}
            if not isinstance(metadata, dict):
                raise SourceError("invalid_response")
            cursor = metadata.get("next_cursor") or page.get("next_cursor")
        if not cursor:
            if page.get("has_more"):
                raise SourceError("invalid_response")
            return
        if not isinstance(cursor, str) or cursor in seen:
            raise SourceError("invalid_response")
        seen.add(cursor)
        position.update({token_key: cursor, offset_key: 0})
        window.save()


def start_time(rooms, room, since):
    cursor = rooms.get(room, {}).get("cursor")
    if not cursor:
        return since
    try:
        stamp = datetime.fromisoformat(cursor)
        if stamp.utcoffset() is not None:
            return max(since, stamp)
    except (TypeError, ValueError):
        pass
    raise SourceError("invalid_cursor")


def iso(stamp):
    return stamp.astimezone(UTC).isoformat()


def display_link(value):
    from agent.redact import redact_sensitive_text

    if not isinstance(value, str) or not value.isprintable():
        return None
    try:
        parts = urlsplit(value)
        if parts.scheme != "https" or not parts.hostname or parts.username or parts.password:
            return None
    except ValueError:
        return None
    return redact_sensitive_text(value, force=True, redact_url_credentials=True)


class Window:
    """An incomplete read keeps its fixed bounds even if the next run starts a day later."""

    def __init__(self, rooms, room, since, until, checkpoint):
        self.room = room
        self.checkpoint = checkpoint
        self.previous = rooms.get(room, {})
        self.position = dict(self.previous.get("pending") or {})
        if self.position:
            self.start = datetime.fromisoformat(self.position["since"])
            self.end = datetime.fromisoformat(self.position["until"])
        else:
            self.start = start_time(rooms, room, since)
            self.end = until
            self.position = {"since": iso(self.start), "until": iso(until)}

    def save(self):
        pending = getattr(self.checkpoint, "pending", None)
        if pending:
            pending(self.room, self.position)

    def finish(self):
        since = iso(self.start)
        if self.previous.get("cursor") and datetime.fromisoformat(self.previous["cursor"]) == self.start:
            since = self.previous.get("since", since)
        self.checkpoint(self.room, iso(self.end), since)


def _composio(tool, params, account):
    args = ["composio", "execute", tool, "-d", json.dumps(params, ensure_ascii=False)]
    if account:
        args.extend(["--account", account])
    # CLI execute otherwise writes responses over 40 KB to an artifact and prints a file pointer.
    env = {**os.environ, "COMPOSIO_CLI_INVOCATION_ORIGIN": "run", "COMPOSIO_CLI_TELEMETRY_DISABLED": "1"}
    try:
        result = subprocess.run(args, stdin=subprocess.DEVNULL, capture_output=True,
                                timeout=30, check=False, env=env)
    except (OSError, subprocess.TimeoutExpired):
        raise SourceError("provider_error") from None
    if result.returncode:
        raise failure((result.stderr + b"\n" + result.stdout).decode("utf-8", errors="replace"))
    try:
        return payload(json.loads(result.stdout))
    except (ValueError, UnicodeError):
        raise SourceError("invalid_response") from None


class _NoRedirects:
    @staticmethod
    def request(method, url, **kwargs):
        import requests

        return requests.request(method, url, allow_redirects=False, **kwargs)


def _connector(tool, params, account):
    from tools.connectors.gateway import wire
    from tools.connectors.gateway.client import ConnectorClient
    from tools.connectors.gateway.errors import ToolGatewayError

    arguments = {**params, "connector_alias": account} if account else params
    body = wire.ConnectorExecuteSingleRequest(call=wire.ConnectorExecuteCall(
        connector="slack", tool=tool, arguments=arguments,
    )).model_dump(by_alias=True, exclude_none=True)
    client = ConnectorClient(transport=_NoRedirects())
    # execute() can retry, fall back to another wire format and log provider error codes/IDs.
    # The collector has already reserved exactly one attempt. Use its authenticated one-shot seam.
    try:
        response = client._send("POST", client._url(wire.CONNECTOR_EXECUTE_PATH), body,
                                timeout=30, idempotency_key=str(uuid.uuid4()), retry_transport=False)
    except ToolGatewayError as exc:
        raise failure(exc.code, exc.status) from None
    try:
        data = response.json()
    except ValueError:
        data = None
    if not 200 <= response.status_code < 300:
        raise failure(data, response.status_code)
    return payload(data)


def invoke(kind, tool, params, account=None):
    check_tool(kind, tool)
    if kind == "mail":
        return _composio(tool, params, account)
    return _connector(tool, params, account)


def collect(kind, config, since, until, rooms, call, emit, checkpoint, *, probe=False):
    from . import mail, slack

    if kind not in TOOLS:
        raise SourceError("tool_denied")
    count = 0

    def spool(message):
        nonlocal count
        if count >= MESSAGE_LIMIT:
            raise SourceError("message_limit")
        emit(message)
        count += 1

    adapter = mail if kind == "mail" else slack
    adapter.collect(config, since, until, rooms, call, spool, checkpoint, probe=probe)
