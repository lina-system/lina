"""A wake receipted before a restart drain must run exactly once on the authority route too.

Once a chat has a committed human turn, ``deliver_wake`` commits a Kanban wake to the session
authority's FIFO (``admit_route_wake``) instead of the classic adapter path the sibling file covers.
The receipt lets the notifier advance its cursor, so the wake is never re-sent: a drain must leave
the committed row queued, and the restarted gateway's startup recovery must run it, never twice. The
test holds the route's FIFO claim while it commits the wake and sets the restart flag, so the
scheduler cannot start the wake first and the assertion checks order instead of racing it. Real
GatewayRunner, DiscordAdapter and SessionAuthority over SQLite; the restart is a second runner and
authority over the same state DB.
"""

import asyncio

import pytest

from tests.waiting import async_wait_until

WAKE = "[Kanban task completed] t_1"


class Hold:
    """Keeps the human turn running until ``finish`` is set."""

    def __init__(self):
        self.running, self.finish = asyncio.Event(), asyncio.Event()


async def authority_route(monkeypatch, instance_id, executed, hold=None):
    from gateway.config import GatewayConfig, Platform, PlatformConfig
    from gateway.platforms.base import SendResult
    from gateway.run import GatewayRunner
    from gateway.session_authority import initialize_session_authority
    from gateway.session_results import execution_result
    from plugins.platforms.discord.adapter import DiscordAdapter

    monkeypatch.setenv("DISCORD_ALLOWED_USERS", "42")
    runner = GatewayRunner(GatewayConfig())
    adapter = DiscordAdapter(PlatformConfig(enabled=True, token="fixture-token", typing_indicator=False))

    async def send(chat_id, content, reply_to=None, metadata=None):
        return SendResult(success=True, message_id="sent")

    monkeypatch.setattr(adapter, "send", send)
    adapter.gateway_runner = runner  # as GatewayRunner._create_adapter binds every adapter
    runner.adapters = {Platform.DISCORD: adapter}
    authority = await initialize_session_authority(runner, profile_id="default", instance_id=instance_id)
    runner._wire_adapter_handlers(adapter)

    async def turn(event, source, quick_key, run_generation):
        executed.append((instance_id, event.text))
        if hold is not None and event.text == "A":
            hold.running.set()
            await hold.finish.wait()
        captured = execution_result.get()
        if captured is not None:
            captured["result"] = {"final_response": "reply " + event.text, "messages": []}
        return "reply " + event.text

    monkeypatch.setattr(runner, "_handle_message_with_agent", turn)
    return runner, adapter, authority, adapter.build_source(chat_id="42", chat_type="dm", user_id="42")


def owner_id(runner, source):
    return runner.session_store.peek_session_id(runner.session_store._generate_session_key(source))


def admissions(runner, authority, source):
    from hermes_state_runtime import list_session_admissions

    rows = list_session_admissions(authority.db, session_id=owner_id(runner, source), pending_only=False)
    return [(row["principal_id"].split(":")[0], row["status"], row.get("outcome")) for row in rows]


def wake_runs(executed):
    return [owner for owner, text in executed if text == WAKE]


def live_route(runner, authority, source):
    return authority.sessions[owner_id(runner, source)]


@pytest.mark.asyncio
@pytest.mark.parametrize("busy", [False, True], ids=["idle", "busy"])
async def test_authority_route_runs_a_wake_receipted_before_drain_once_after_restart(monkeypatch, busy):
    from gateway.platforms.event import MessageEvent
    from gateway.run_runtime import recover_profile_native_sessions
    from gateway.wake import deliver_wake

    executed, hold = [], Hold() if busy else None
    runner, adapter, authority, source = await authority_route(monkeypatch, "owner", executed, hold)
    await adapter.handle_message(MessageEvent(text="A", source=source, message_id="A"))
    if hold is not None:
        await asyncio.wait_for(hold.running.wait(), 10)
    else:
        await async_wait_until(
            lambda: admissions(runner, authority, source) == [("messaging", "terminal", "completed")],
            what="the human turn to settle",
        )
    # The FIFO claim holds the route's ``claim_gate``, so the drain this admission schedules cannot
    # start the wake until the restart flag is set: order decides, not the runner's speed.
    live = live_route(runner, authority, source)
    await live.claim_gate.acquire()
    try:
        await asyncio.wait_for(deliver_wake(adapter, text=WAKE, source=source), 10)  # receipt: no retry
        runner._draining = True  # /restart begins before the wake's turn is claimed
    finally:
        live.claim_gate.release()
    if hold is not None:
        hold.finish.set()
    await async_wait_until(
        lambda: admissions(runner, authority, source)[0] == ("messaging", "terminal", "completed"),
        what="the human turn to settle",
    )
    wake_row = admissions(runner, authority, source)[-1]
    assert wake_row[:2] == ("automation", "queued")  # receipted, and never claimed under the drain
    assert wake_runs(executed) == []

    successor, _adapter, successor_authority, _source = await authority_route(monkeypatch, "successor", executed)
    await recover_profile_native_sessions(successor, successor_authority)  # a real gateway's startup step
    await async_wait_until(
        lambda: admissions(successor, successor_authority, source)[-1] == ("automation", "terminal", "completed"),
        what="the wake to run",
    )
    assert wake_runs(executed) == ["successor"]  # the restarted gateway ran the receipted wake, once
