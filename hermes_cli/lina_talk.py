"""Talk with the agent that owns a to-do (lina#167): find the item and its agent in the work record, run the owner's
message as one turn of that agent's app conversation on its gateway, and relay the turn's events.

Which conversation: the agent's own conversation with the owner on a messaging platform cannot take a turn from
another surface; the gateway refuses an API turn into a session whose source is not ``api_server``
(``gateway/session_api_turn.py`` ``admit_api_steps``, ``permission_denied``). So each agent has one app
conversation, an API session titled ``LINA app: <agent>`` on the agent's own gateway, reached through the same
door ``hermes peer dm`` uses (``POST /api/sessions/{id}/chat/stream``; a multiplexed gateway serves a secondary at
``/p/<profile>/``). The gateway is the only writer of that session and a busy agent answers with the gateway's own
response. The turn's author is the owner; its first line names the app and the to-do. After a reply the exchange is
left as a note the agent's next messaging DM turn reads (``gateway/app_talk_carry.py``).

The owner's window per agent (lina#482) runs turns in the same app conversation with no to-do: :func:`resolve_agent`
names the agent, :func:`history` reads the conversation's earlier turns, and no note is carried into the messenger
chat. The gateway key is the web server's ``LINA_GATEWAY_KEY_<AGENT>``, else its ``API_SERVER_KEY``; a refused key
answers :data:`REFUSED_KEY` and never the key itself.
"""

from __future__ import annotations

import json
import urllib.parse
from dataclasses import dataclass
from pathlib import Path
from typing import AsyncIterator, Optional

MAX_CHARS = 4000
SESSION_TITLE = "LINA app: {agent}"
_CRON_FIRE_PATH = "/api/cron/fire"
KEY_ENV = "LINA_GATEWAY_KEY_{agent}"
REFUSED_KEY = "the gateway refused the key for {agent}"


class TalkError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


@dataclass(frozen=True)
class Target:
    """Who the turn goes to; ``item`` is ``""`` for the agent's own window (no to-do)."""
    item: str
    title: str
    agent: str
    owner: str
    owner_name: str

    @property
    def author(self) -> dict:
        return {"id": self.owner, "name": self.owner_name, "is_bot": False}

    def turn_text(self, message: str) -> str:
        if not self.item:
            return f"[LINA app]\n\n{message}"
        return f'[LINA app · to-do {self.item} "{self.title}"]\n\n{message}'


def check_message(message) -> str:
    if not isinstance(message, str) or not message.strip():
        raise TalkError(400, "empty_message", "message is empty")
    if len(message) > MAX_CHARS:
        raise TalkError(413, "message_too_long", f"message is longer than {MAX_CHARS} characters")
    return message.strip()


def resolve(item_id: str) -> Target:
    """The item and the agent it answers to (``work.board.agent_of``), in one bound read of the record."""
    from hermes_cli.lina_board import _WORK_LOCK
    from hermes_cli.lina_ocx import lina_root
    from hermes_cli.work import env, ledger
    from hermes_cli.work.board import agent_of
    from hermes_cli.work.evaluate import item_name
    with _WORK_LOCK:
        env.bind(env.load(lina_root()))
        try:
            recs, _order = ledger.read_all()
            item = recs.get(item_id)
            if item is None:
                raise TalkError(404, "unknown_item", f"no item {item_id}")
            agent = agent_of(item, recs)
            if not agent:
                raise TalkError(409, "no_owning_agent", f"item {item_id} has no owning agent")
            current = env.current()
            return Target(item_id, item_name(item), agent, current.owner, current.owner_name)
        finally:
            ledger.close()
            env.reset()


def resolve_agent(agent: str) -> Target:
    """The owner's window on *agent*: one of the record's agents (``work.agents``), else 404."""
    from hermes_cli.lina_ocx import lina_root
    from hermes_cli.work import env
    current = env.load(lina_root())
    if agent not in current.agents:
        raise TalkError(404, "unknown_agent", f"no agent {agent}")
    return Target("", "", agent, current.owner, current.owner_name)


def profile_home(agent: str) -> Path:
    from hermes_cli.profiles import get_profile_dir
    return get_profile_dir(agent)


def gateway(agent: str) -> tuple[str, str]:
    """``(base URL, API key)`` of the agent's gateway api_server on loopback: the port the cron forwarder resolves
    (``/p/<agent>`` on a multiplexed default listener), the key from this process's ``LINA_GATEWAY_KEY_<AGENT>``
    (the agent name in upper case), else its shared ``API_SERVER_KEY``."""
    import os
    from hermes_cli.web_server_cron import _gateway_fire_endpoint
    base = _gateway_fire_endpoint(agent, profile_home(agent)).removesuffix(_CRON_FIRE_PATH)
    key = os.environ.get(KEY_ENV.format(agent=agent.upper()))
    if key is None:
        key = os.environ.get("API_SERVER_KEY", "")
    return base, key.strip()


def http_client():
    import httpx
    return httpx.AsyncClient(timeout=httpx.Timeout(30.0, read=None))


async def _find_session(client, base: str, headers: dict, agent: str) -> Optional[str]:
    """The agent's app conversation, ``None`` before its first turn."""
    title = SESSION_TITLE.format(agent=agent)
    query = urllib.parse.urlencode({"limit": 200, "title": title, "include_hidden": 1})
    listing = await client.get(f"{base}/api/sessions?{query}", headers=headers)
    listing.raise_for_status()
    for row in listing.json().get("data") or []:
        if isinstance(row, dict) and (row.get("title") or "").strip() == title and row.get("id"):
            return str(row["id"])
    return None


async def _session(client, base: str, headers: dict, agent: str) -> str:
    """The agent's app conversation, created on first use (the find-then-create of ``hermes peer dm``)."""
    found = await _find_session(client, base, headers, agent)
    if found:
        return found
    title = SESSION_TITLE.format(agent=agent)
    created = await client.post(f"{base}/api/sessions", headers=headers, json={"title": title, "source": "api_server"})
    created.raise_for_status()
    body = created.json()
    session = body.get("session") if isinstance(body.get("session"), dict) else body
    return str(session["id"])


@dataclass
class Opened:
    """An upstream answer: ``stream`` is set when the turn runs; otherwise ``status``/``body`` are the gateway's."""
    status: int
    body: bytes = b""
    media_type: str = "application/json"
    stream: Optional[AsyncIterator[bytes]] = None


def _frame(event: str, data: dict) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n".encode()


def _error_body(code: str, message: str) -> bytes:
    return json.dumps({"error": {"code": code, "message": message}}).encode()


def _upstream(agent: str, status: int, body: bytes, media_type: str) -> Opened:
    """The gateway's own answer, except a refused key: the gateway's 401 body is never relayed, and the dashboard
    client must not read it as its own sign-in expiring."""
    if status == 401:
        return Opened(502, _error_body("gateway_refused_key", REFUSED_KEY.format(agent=agent)))
    return Opened(status, body, media_type)


def _headers(agent: str) -> tuple[str, dict]:
    base, key = gateway(agent)
    return base, {"Authorization": f"Bearer {key}"} if key else {}


async def history(target: Target) -> Opened:
    """The earlier turns of the agent's app conversation, oldest first: ``{agent, session_id, messages}`` with the
    owner's and the agent's messages (``role``, ``content``, ``timestamp``); none before the first turn."""
    import httpx
    base, headers = _headers(target.agent)
    async with http_client() as client:
        try:
            session_id = await _find_session(client, base, headers, target.agent)
            rows = []
            if session_id:
                resp = await client.get(f"{base}/api/sessions/{urllib.parse.quote(session_id, safe='')}/messages",
                                        params={"order": "oldest", "limit": 500}, headers=headers)
                resp.raise_for_status()
                rows = resp.json().get("data") or []
        except httpx.HTTPStatusError as exc:
            return _upstream(target.agent, exc.response.status_code, exc.response.content,
                             exc.response.headers.get("content-type", "application/json"))
        except httpx.HTTPError as exc:
            return Opened(503, _error_body("gateway_unreachable", f"{target.agent}'s gateway: {type(exc).__name__}"))
    messages = [{"role": row["role"], "content": row.get("content") or "", "timestamp": row.get("timestamp")}
                for row in rows if isinstance(row, dict) and row.get("role") in ("user", "assistant")]
    body = {"agent": target.agent, "session_id": session_id, "messages": messages}
    return Opened(200, json.dumps(body, ensure_ascii=False).encode())


async def open_talk(target: Target, message: str) -> Opened:
    """Start the turn; the gateway's own status and body when it does not stream (busy, unreachable), and
    :data:`REFUSED_KEY` when it refuses the key."""
    import httpx
    base, headers = _headers(target.agent)
    client = http_client()
    try:
        session_id = await _session(client, base, headers, target.agent)
        request = client.build_request(
            "POST", f"{base}/api/sessions/{urllib.parse.quote(session_id, safe='')}/chat/stream", headers=headers,
            json={"message": target.turn_text(message), "author": target.author})
        upstream = await client.send(request, stream=True)
    except httpx.HTTPStatusError as exc:
        await client.aclose()
        return _upstream(target.agent, exc.response.status_code, exc.response.content,
                         exc.response.headers.get("content-type", "application/json"))
    except httpx.HTTPError as exc:
        await client.aclose()
        return Opened(503, _error_body("gateway_unreachable", f"{target.agent}'s gateway: {type(exc).__name__}"))
    if upstream.status_code != 200:
        body = await upstream.aread()
        await upstream.aclose()
        await client.aclose()
        return _upstream(target.agent, upstream.status_code, body,
                         upstream.headers.get("content-type", "application/json"))
    return Opened(200, stream=_relay(client, upstream, target, message, session_id))


async def _events(upstream) -> AsyncIterator[tuple[Optional[str], str]]:
    """``(event, data)`` per SSE frame; ``(None, comment)`` for a keepalive comment."""
    event, data = None, []
    async for line in upstream.aiter_lines():
        if line.startswith(":"):
            yield None, line
        elif line.startswith("event:"):
            event = line[6:].strip()
        elif line.startswith("data:"):
            data.append(line[5:].lstrip())
        elif not line and (event or data):
            yield event, "\n".join(data)
            event, data = None, []


def _settled_reply(data: str) -> str:
    """The reply of an ``assistant.completed`` frame for a turn that finished (``completed: true``, the gateway's
    ``terminal_run_status``); ``""`` for a failed, partial or cancelled turn."""
    try:
        payload = json.loads(data)
    except ValueError:
        return ""
    if not isinstance(payload, dict) or payload.get("completed") is not True:
        return ""
    return payload.get("content") or ""


async def _relay(client, upstream, target: Target, message: str, session_id: str) -> AsyncIterator[bytes]:
    """``talk.started``, then the gateway's frames as they are, with ``talk.carried`` after the reply's
    ``assistant.completed``. The note for the agent's next messaging turn is written before that frame is yielded:
    a client that goes away at any yield (the generator is closed there) can no longer skip it."""
    import asyncio
    from gateway.app_talk_carry import write_carry
    try:
        started = {"agent": target.agent, "session_id": session_id}
        if target.item:
            started = {"item": {"id": target.item, "title": target.title}, **started}
        yield _frame("talk.started", started)
        async for event, data in _events(upstream):
            if event is None:
                yield (data + "\n\n").encode()
                continue
            reply = _settled_reply(data) if event == "assistant.completed" and target.item else ""
            if reply:
                note = {"item": target.item, "title": target.title, "message": message, "reply": reply,
                        "session_id": session_id}
                await asyncio.to_thread(write_carry, profile_home(target.agent), note)
            yield f"event: {event}\ndata: {data}\n\n".encode()
            if reply:
                yield _frame("talk.carried", {"agent": target.agent, "item": target.item})
    finally:
        await upstream.aclose()
        await client.aclose()
