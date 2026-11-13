"""``outbound.allow`` follows the routed profile through post-handler reply delivery (lina#638).

Under multiplexing the runner's message handler runs the turn in the routed profile's scope and
leaves it when it returns; the adapter then sends the final text, attachments and inline replies.
Those sends must be checked against the routed profile's list, not the launch profile's. Each test
drives a real delivery path with a fake transport and counts what reached it.
"""

from __future__ import annotations

import asyncio
import json
from pathlib import Path

import pytest

from hermes_constants import get_hermes_home
from tests.agent.test_outbound_lock_doors import _fake_telegram


def _write_allow(home: Path, entries) -> None:
    home.mkdir(parents=True, exist_ok=True)
    outbound = {"allow": list(entries)} if entries is not None else {}
    (home / "config.yaml").write_text(json.dumps({"outbound": outbound}), encoding="utf-8")  # JSON is YAML


def _routed_event(launch: Path, routed: Path):
    """An inbound DM to chat 222 and the runner-shaped handler that routes it to ``routed``: it pins
    the identity, runs the turn in the routed scope and leaves that scope when it returns."""
    from gateway.config import Platform
    from gateway.platforms.event import MessageEvent, MessageType
    from gateway.session import SessionSource
    from gateway.session_identity import _IDENTITY_ATTR, RoutingIdentity
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override

    source = SessionSource(platform=Platform.TELEGRAM, chat_id="222", chat_type="dm", user_id="u1")
    event = MessageEvent(text="hello", source=source, message_type=MessageType.TEXT)

    async def handler(event):
        setattr(event.source, _IDENTITY_ATTR, RoutingIdentity(
            transport_profile="default", runtime_profile="beta", authorization_home=launch, runtime_home=routed))
        token = set_hermes_home_override(str(routed))
        try:
            return "the answer"
        finally:
            reset_hermes_home_override(token)

    return event, handler


async def _background(adapter, event):
    await adapter.handle_message(event)
    await asyncio.gather(*list(adapter._session_tasks.values()))


async def _inline(adapter, event):
    await adapter._dispatch_inline_reply(event)


async def _shared_busy(adapter, event):
    from gateway.session_ingress import dispatch_shared_busy

    await dispatch_shared_busy(adapter, event, "agent:beta:telegram:dm:222")
    await asyncio.gather(*list(adapter._background_tasks))


_PATHS = {"final_text": _background, "inline_reply": _inline, "shared_busy_reply": _shared_busy}


@pytest.mark.parametrize("path", sorted(_PATHS))
@pytest.mark.parametrize("launch_allow, routed_allow, sends", [
    (None, ["telegram:111"], 0),  # unrestricted launch profile must not let the routed one's refusal out
    (["telegram:111"], None, 1),  # restricted launch profile must not block what the routed one allows
])
def test_post_handler_delivery_follows_the_routed_profile(tmp_path, path, launch_allow, routed_allow, sends):
    launch, routed = get_hermes_home(), tmp_path / "profiles" / "beta"
    _write_allow(launch, launch_allow)
    _write_allow(routed, routed_allow)
    adapter = _fake_telegram()
    event, adapter._message_handler = _routed_event(launch, routed)
    asyncio.run(_PATHS[path](adapter, event))
    assert [call for call in adapter.calls if call[0] == "send"] == [("send", "222", "the answer")] * sends
