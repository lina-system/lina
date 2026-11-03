"""Regression for lina#208: do not cancel an accepted final before its ack."""

import asyncio
from types import SimpleNamespace

import pytest

from gateway import stream_consumer
from gateway.config import Platform, PlatformConfig
from gateway.platforms.base import BasePlatformAdapter, SendResult
from gateway.run_turn import GatewayTurnMixin
from gateway.stream_consumer import GatewayStreamConsumer, StreamConsumerConfig


class DelayedAckAdapter(BasePlatformAdapter):
    """Receipt precedes acknowledgement, like a slow Telegram sendMessage."""

    def __init__(self):
        super().__init__(PlatformConfig(enabled=True), Platform.TELEGRAM)
        self.received = []
        self.entered = asyncio.Event()
        self.ack = asyncio.Event()
        self.cancelled = False

    async def connect(self, *, is_reconnect=False):
        return True

    async def disconnect(self):
        return None

    async def send(self, chat_id, content, reply_to=None, metadata=None):
        self.received.append(content)
        if len(self.received) == 1:
            self.entered.set()
            try:
                await self.ack.wait()
            except asyncio.CancelledError:
                self.cancelled = True
                raise
        return SendResult(success=True, message_id=str(len(self.received)))

    async def send_typing(self, chat_id, metadata=None):
        return None

    async def get_chat_info(self, chat_id):
        return {"id": chat_id}


@pytest.fixture
def flush_budget(monkeypatch):
    """Observe the real wait boundary, not a wall-clock race with an ack timer."""
    monkeypatch.setattr(stream_consumer, "STREAM_FLUSH_TIMEOUT", 0.0, raising=False)
    monkeypatch.setattr(stream_consumer, "STREAM_FINAL_SEND_GRACE", 0.2, raising=False)
    original_wait = asyncio.wait
    original_wait_for = asyncio.wait_for
    state = SimpleNamespace(task=None, expired=asyncio.Event(), release=None, waits=[])

    async def wait(tasks, *, timeout=None, **kwargs):
        result = await original_wait(tasks, timeout=timeout, **kwargs)
        if state.task in tasks:
            state.waits.append(timeout)
            if not result[0]:
                state.expired.set()
                if state.release is not None:
                    state.release.set()
        return result

    async def wait_for(awaitable, timeout):
        # Baseline has a literal 5s wait_for; shorten only that exact task.
        if awaitable is state.task:
            timeout = stream_consumer.STREAM_FLUSH_TIMEOUT
            state.expired.set()
        return await original_wait_for(awaitable, timeout)

    monkeypatch.setattr(asyncio, "wait", wait)
    monkeypatch.setattr(asyncio, "wait_for", wait_for)
    return state


async def flush_turn(consumer, task):
    """Exercise the actual turn-cleanup caller and delivery suppression rule."""
    runner = GatewayTurnMixin()
    runner._draining = False
    tracking = asyncio.create_task(asyncio.Event().wait())
    ctx = SimpleNamespace(
        stream_consumer_holder=[consumer], streaming_tts_consumer_holder=[None],
        session_key=None, source=SimpleNamespace(platform=Platform.TELEGRAM),
    )
    await runner._run_agent_cleanup_turn_tasks(
        ctx, progress_task=None, log_task=None, interrupt_monitor=None,
        _notify_task=None, tracking_task=tracking, stream_task=task,
    )
    response = {"final_response": "The final answer."}
    await runner._run_agent_mark_streamed_delivery(response, ctx)
    if not response.get("already_sent"):
        await consumer.adapter.send(consumer.chat_id, response["final_response"])
    return response


@pytest.mark.asyncio
async def test_final_ack_after_flush_budget_suppresses_resend(flush_budget):
    # Given: the remote has accepted the final, but its ack has not arrived.
    adapter = DelayedAckAdapter()
    consumer = GatewayStreamConsumer(adapter, "chat", StreamConsumerConfig(cursor=""))
    consumer.on_delta("The final answer.")
    consumer.finish("The final answer.")
    task = asyncio.create_task(consumer.run())
    flush_budget.task = task
    flush_budget.release = adapter.ack
    await asyncio.wait_for(adapter.entered.wait(), 1)

    # When: the initial budget expires; only then may the ack arrive.
    response = await flush_turn(consumer, task)

    # Then: the gateway observes the ack instead of delivering a duplicate.
    assert adapter.received == ["The final answer."]
    assert response.get("already_sent") is True
    assert flush_budget.expired.is_set()
    assert not adapter.cancelled
    assert not consumer.final_send_in_flight


@pytest.mark.asyncio
async def test_pre_final_stall_is_cancelled_at_flush_budget(flush_budget):
    # Given: a preview send is blocked, before the consumer handles done.
    adapter = DelayedAckAdapter()
    consumer = GatewayStreamConsumer(
        adapter, "chat", StreamConsumerConfig(cursor="", buffer_threshold=1),
    )
    consumer.on_delta("Still working.")
    task = asyncio.create_task(consumer.run())
    flush_budget.task = task
    await asyncio.wait_for(adapter.entered.wait(), 1)
    consumer.finish("The final answer.")

    # When: the flush budget expires while the preview still owns the transport.
    response = await flush_turn(consumer, task)

    # Then: no grace is spent on a non-final call, and the final fallback runs.
    assert adapter.cancelled
    assert task.done()
    assert flush_budget.waits == [0.0]
    assert not response.get("already_sent")
    assert adapter.received == ["Still working.", "The final answer."]


@pytest.mark.asyncio
async def test_final_grace_expiry_keeps_normal_resend(flush_budget, monkeypatch):
    # Given: even the final-send grace expires without a transport ack.
    monkeypatch.setattr(DelayedAckAdapter, "STREAM_FINAL_SEND_GRACE", 0.0, raising=False)
    adapter = DelayedAckAdapter()
    consumer = GatewayStreamConsumer(adapter, "chat", StreamConsumerConfig(cursor=""))
    consumer.on_delta("The final answer.")
    consumer.finish("The final answer.")
    task = asyncio.create_task(consumer.run())
    flush_budget.task = task
    await asyncio.wait_for(adapter.entered.wait(), 1)

    # When: both waits expire, preserving the existing ambiguous-send policy.
    response = await flush_turn(consumer, task)

    # Then: a possible duplicate is preferred to losing the reply.
    assert adapter.cancelled
    assert flush_budget.waits == [0.0, 0.0]
    assert not response.get("already_sent")
    assert adapter.received == ["The final answer.", "The final answer."]
    assert not consumer.final_send_in_flight
