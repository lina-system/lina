"""Every egress door refuses a target ``outbound.allow`` does not list (lina#638).

Each test drives a real door with a fake transport and counts what reached the transport: a refused
target makes zero outbound calls, an allowed target exactly one.
"""

from __future__ import annotations

import asyncio
import json
import subprocess
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

from agent.outbound_lock import read_refusals
from hermes_constants import get_hermes_home
from tests.waiting import async_wait_until


def _allow(*entries: str) -> None:
    config = get_hermes_home() / "config.yaml"
    config.parent.mkdir(parents=True, exist_ok=True)
    config.write_text(json.dumps({"outbound": {"allow": list(entries)}}), encoding="utf-8")  # JSON is YAML


def _fake_telegram():
    from gateway.config import Platform, PlatformConfig
    from gateway.platforms.base import BasePlatformAdapter, SendResult

    class FakeTelegram(BasePlatformAdapter):
        async def connect(self, *, is_reconnect: bool = False) -> bool:
            return True

        async def disconnect(self) -> None:
            return None

        async def get_chat_info(self, chat_id):
            return {}

        async def send(self, chat_id, content, reply_to=None, metadata=None):
            self.calls.append(("send", chat_id, content))
            return SendResult(success=True, message_id=f"m{len(self.calls)}")

        async def edit_message(self, chat_id, message_id, content, *, finalize=False):
            self.calls.append(("edit", chat_id, content))
            return SendResult(success=True, message_id=message_id)

    adapter = FakeTelegram(PlatformConfig(enabled=True), Platform.TELEGRAM)
    adapter.calls = []
    return adapter


@pytest.mark.parametrize("chat, calls", [("111", 1), ("222", 0)])
def test_adapter_send(chat, calls):
    _allow("telegram:111")
    adapter = _fake_telegram()
    result = asyncio.run(adapter._send_with_retry(chat, "hello"))
    assert len(adapter.calls) == calls and result.success is bool(calls)


@pytest.mark.parametrize("chat, calls", [("111", 1), ("222", 0)])
def test_adapter_edit(chat, calls):
    _allow("telegram:111")
    adapter = _fake_telegram()
    result = asyncio.run(adapter.edit_message(chat, "m1", "new text"))
    assert len(adapter.calls) == calls and result.success is bool(calls)


def test_a_stream_whose_chat_leaves_the_list_stops_without_a_fallback_send():
    from gateway.stream_consumer import GatewayStreamConsumer, StreamConsumerConfig

    async def scenario(adapter):
        consumer = GatewayStreamConsumer(adapter, "111", StreamConsumerConfig(edit_interval=0.01, cursor=""))
        task = asyncio.create_task(consumer.run())
        consumer.on_delta("first words")
        await async_wait_until(lambda: adapter.calls, what="first send")
        _allow("telegram:999")
        consumer.on_delta(" and the rest of the answer")
        consumer.finish()
        await asyncio.wait_for(task, 10)
        return consumer

    _allow("telegram:111")
    adapter = _fake_telegram()
    consumer = asyncio.run(scenario(adapter))
    assert [call[0] for call in adapter.calls] == ["send"]
    assert consumer._egress_declined
    assert read_refusals()["total"] == 1


@pytest.mark.parametrize("chat, calls", [("111", 1), ("222", 0)])
def test_send_message(chat, calls):
    from gateway.config import Platform, PlatformConfig
    from tools.send_message_tool import send_message_tool

    _allow("telegram:111")
    config = SimpleNamespace(platforms={Platform.TELEGRAM: PlatformConfig(enabled=True, token="t")},
                             get_home_channel=lambda platform: None)
    sender = AsyncMock(return_value={"success": True, "message_id": "1"})
    with patch("gateway.config.load_gateway_config", return_value=config), \
         patch("tools.send_message_tool._send_telegram", new=sender):
        result = json.loads(send_message_tool({"action": "send", "target": f"telegram:{chat}", "message": "hi"}))
    assert sender.await_count == calls
    assert ("outbound.allow" in result.get("error", "")) is not bool(calls)


@pytest.mark.parametrize("chat, calls", [("111", 1), ("222", 0)])
def test_cron_delivery(chat, calls):
    from gateway.config import Platform, PlatformConfig
    from cron.scheduler import _deliver_result

    _allow("telegram:111")
    config = SimpleNamespace(platforms={Platform.TELEGRAM: PlatformConfig(enabled=True, token="t")},
                             get_home_channel=lambda platform: None)
    send = AsyncMock(return_value={"success": True, "message_id": "1"})
    job = {"id": "brief", "name": "brief", "deliver": f"telegram:{chat}"}
    with patch("gateway.config.load_gateway_config", return_value=config), \
         patch("tools.send_message_tool._send_to_platform", new=send):
        error = _deliver_result(job, "report")
    assert send.await_count == calls
    assert (error is None) is bool(calls)


@pytest.mark.parametrize("handler", ["_peer_dm", "_peer_run"])
@pytest.mark.parametrize("peer, calls", [("friend", 1), ("stranger", 0)])
def test_peer_dm_and_run(handler, peer, calls):
    from hermes_cli.subcommands import peer as peer_cmd

    _allow("agent:friend")
    request = []

    def fake_request(url, key, **kwargs):
        request.append(url)
        return {"message": {"content": "ok"}, "run_id": "r1", "status": "started"}

    args = SimpleNamespace(json=True, idempotency_key=None)
    with patch.object(peer_cmd, "_ensure_bot_chat", return_value="s1"), \
         patch.object(peer_cmd, "_peer_run_durability", return_value=True), \
         patch.object(peer_cmd, "_request", side_effect=fake_request):
        code = getattr(peer_cmd, handler)(args, "hi", peer, None, "http://peer", "k")
    assert len(request) == calls and (code == 0) is bool(calls)


@pytest.mark.parametrize("target, calls", [("scout", 1), ("other", 0)])
def test_message_agent(tmp_path: Path, target, calls):
    from tools import bot_mode_dm, bot_mode_probe

    _allow("agent:scout")
    roster = [(name, tmp_path / name) for name in ("me", "other", "scout")]
    deliveries = []

    def fake_start_delivery(argv, content, label, **kwargs):
        deliveries.append(argv)
        return json.dumps({"status": "dispatched"})

    with patch.object(bot_mode_dm, "_session_title", return_value=bot_mode_probe.BOT_CHAT_TITLE), \
         patch.object(bot_mode_dm, "_agent_home", return_value=str(tmp_path / "me")), \
         patch.object(bot_mode_probe, "is_bot_mode_managed", return_value=True), \
         patch.object(bot_mode_probe, "_hermes_root", return_value=tmp_path), \
         patch.object(bot_mode_probe, "_profile_name", return_value="me"), \
         patch.object(bot_mode_probe, "_roster", return_value=roster), \
         patch.object(bot_mode_probe, "_peers", return_value=[]), \
         patch.object(bot_mode_dm, "_try_relay_delivery", return_value=None), \
         patch.object(bot_mode_dm, "_start_delivery", side_effect=fake_start_delivery), \
         patch("hermes_cli.observability.shared_metrics_signals.record_feature_used"):
        result = json.loads(bot_mode_dm.message_agent_tool(target=target, message="hello"))
    assert len(deliveries) == calls
    assert ("outbound.allow" in result.get("error", "")) is not bool(calls)


@pytest.mark.parametrize("pr, calls", [("4", 1), ("5", 0)])
def test_webhook_github_comment(pr, calls):
    from gateway.config import PlatformConfig
    from gateway.platforms.webhook import WebhookAdapter

    _allow("github_comment:owner/repo#4")
    ran = []

    def fake_run(argv, **kwargs):
        ran.append(argv)
        return subprocess.CompletedProcess(argv, 0, stdout="", stderr="")

    adapter = WebhookAdapter(PlatformConfig(enabled=True))
    delivery = {"deliver": "github_comment", "deliver_extra": {"repo": "owner/repo", "pr_number": pr}}
    with patch("gateway.platforms.webhook.subprocess.run", side_effect=fake_run):
        result = asyncio.run(adapter._deliver_github_comment("review done", delivery))
    assert len(ran) == calls and result.success is bool(calls)


@pytest.mark.parametrize("bot, calls", [("lead", 1), ("someone-else", 0)])
def test_worker_end_notice(tmp_path: Path, bot, calls):
    from hermes_cli.worker import notice

    _allow("agent:lead")
    ran = []

    def fake_run(argv, **kwargs):
        ran.append(argv)
        return subprocess.CompletedProcess(argv, 0, stdout=b"", stderr=b"")

    with patch.object(notice.subprocess, "run", side_effect=fake_run):
        sent = notice.deliver({"id": "t1", "bot": bot}, "done", "end", tmp_path, ["notify", "{agent}"])
    assert len(ran) == calls and sent.ok is bool(calls)
