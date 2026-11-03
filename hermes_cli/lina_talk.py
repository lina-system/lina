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


class TalkError(Exception):
    def __init__(self, status: int, code: str, message: str):
        super().__init__(message)
        self.status, self.code, self.message = status, code, message


@dataclass(frozen=True)
class Target:
    item: str
    title: str
    agent: str
    owner: str
    owner_name: str

    @property
    def author(self) -> dict:
        return {"id": self.owner, "name": self.owner_name, "is_bot": False}

    def turn_text(self, message: str) -> str:
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


def profile_home(agent: str) -> Path:
    from hermes_cli.profiles import get_profile_dir
    return get_profile_dir(agent)


def gateway(agent: str) -> tuple[str, str]:
    """``(base URL, API key)`` of the agent's gateway api_server on loopback: the port the cron forwarder resolves
    (``/p/<agent>`` on a multiplexed default listener), the key from the agent's own ``.env`` (it authorizes
    ``/p/<agent>/`` too), else this process's."""
    import os
    from hermes_cli.web_server_cron import _gateway_fire_endpoint, _profile_env_value
    home = profile_home(agent)
    base = _gateway_fire_endpoint(agent, home).removesuffix(_CRON_FIRE_PATH)
    key = _profile_env_value(home, "API_SERVER_KEY") or os.environ.get("API_SERVER_KEY", "")
    return base, key.strip()


def http_client():
    import httpx
    return httpx.AsyncClient(timeout=httpx.Timeout(30.0, read=None))


async def _session(client, base: str, headers: dict, agent: str) -> str:
    """The agent's app conversation, created on first use (the find-then-create of ``hermes peer dm``)."""
    title = SESSION_TITLE.format(agent=agent)
    query = urllib.parse.urlencode({"limit": 200, "title": title, "include_hidden": 1})
    listing = await client.get(f"{base}/api/sessions?{query}", headers=headers)
    listing.raise_for_status()
    for row in listing.json().get("data") or []:
        if isinstance(row, dict) and (row.get("title") or "").strip() == title and row.get("id"):
            return str(row["id"])
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


async def open_talk(target: Target, message: str) -> Opened:
    """Start the turn; the gateway's own status and body when it does not stream (busy, refused, unreachable)."""
    import httpx
    base, key = gateway(target.agent)
    headers = {"Authorization": f"Bearer {key}"} if key else {}
    client = http_client()
    try:
        session_id = await _session(client, base, headers, target.agent)
        request = client.build_request(
            "POST", f"{base}/api/sessions/{urllib.parse.quote(session_id, safe='')}/chat/stream", headers=headers,
            json={"message": target.turn_text(message), "author": target.author})
        upstream = await client.send(request, stream=True)
    except httpx.HTTPStatusError as exc:
        await client.aclose()
        return Opened(exc.response.status_code, exc.response.content,
                      exc.response.headers.get("content-type", "application/json"))
    except httpx.HTTPError as exc:
        await client.aclose()
        return Opened(503, _error_body("gateway_unreachable", f"{target.agent}'s gateway: {type(exc).__name__}"))
    if upstream.status_code != 200:
        body = await upstream.aread()
        await upstream.aclose()
        await client.aclose()
        return Opened(upstream.status_code, body, upstream.headers.get("content-type", "application/json"))
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


async def _relay(client, upstream, target: Target, message: str, session_id: str) -> AsyncIterator[bytes]:
    """``talk.started``, then the gateway's frames as they are, with ``talk.carried`` after the reply's
    ``assistant.completed`` once the exchange is noted for the agent's next messaging turn."""
    import asyncio
    from gateway.app_talk_carry import write_carry
    try:
        yield _frame("talk.started", {"item": {"id": target.item, "title": target.title}, "agent": target.agent,
                                      "session_id": session_id})
        async for event, data in _events(upstream):
            if event is None:
                yield (data + "\n\n").encode()
                continue
            yield f"event: {event}\ndata: {data}\n\n".encode()
            if event != "assistant.completed":
                continue
            try:
                reply = json.loads(data).get("content") or ""
            except ValueError:
                reply = ""
            if reply:
                note = {"item": target.item, "title": target.title, "message": message, "reply": reply,
                        "session_id": session_id}
                await asyncio.to_thread(write_carry, profile_home(target.agent), note)
                yield _frame("talk.carried", {"agent": target.agent, "item": target.item})
    finally:
        await upstream.aclose()
        await client.aclose()
