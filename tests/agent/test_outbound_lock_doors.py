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
from unittest.mock import AsyncMock, MagicMock, patch

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

        # Inherits send_exec_approval, which renders through this hook (Telegram, Slack, Discord, Relay).
        async def _send_exec_approval_prompt(self, prompt):
            self.calls.append(("approval", prompt.chat_id, prompt.text))
            return SendResult(success=True, message_id="a1")

        async def send_native_task_card_progress(self, chat_id, tasks, *, title=None, reply_to=None,
                                                 metadata=None, fallback_text=None):
            self.calls.append(("task_card", chat_id, tasks))
            return SendResult(success=True)

        async def send_update_prompt(self, chat_id, prompt, default="", session_key="", metadata=None):
            self.calls.append(("update_prompt", chat_id, prompt))
            return SendResult(success=True)

        async def send_model_picker(self, chat_id, providers, current_model, current_provider, session_key,
                                    on_model_selected, metadata=None):
            self.calls.append(("model_picker", chat_id, providers))
            return SendResult(success=True)

        async def send_choice_picker(self, chat_id, title, choices, session_key, on_choice_selected,
                                     metadata=None):
            self.calls.append(("choice_picker", chat_id, title))
            return SendResult(success=True)

        async def play_tts(self, chat_id, audio_path, **kwargs):
            self.calls.append(("play_tts", chat_id, audio_path))
            return SendResult(success=True)

        async def abandon_open_draft(self, chat_id, content, metadata=None):
            self.calls.append(("seal_draft", chat_id, content))
            return SendResult(success=True)

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


# Content doors that do not pass through send(): each must be refused on its own.
_ADAPTER_DOORS = {
    "inherited_exec_approval": lambda adapter, chat: adapter.send_exec_approval(chat, "rm -rf build", "s1"),
    "native_task_card": lambda adapter, chat: adapter.send_native_task_card_progress(
        chat, [{"id": "t1", "title": "read the logs", "status": "in_progress"}]),
    "update_prompt": lambda adapter, chat: adapter.send_update_prompt(chat, "Restart now?"),
    "model_picker": lambda adapter, chat: adapter.send_model_picker(chat, ["p"], "m", "p", "s1", None),
    "choice_picker": lambda adapter, chat: adapter.send_choice_picker(chat, "Reasoning", [], "s1", None),
    "play_tts": lambda adapter, chat: adapter.play_tts(chat, "answer.ogg"),
    "abandon_open_draft": lambda adapter, chat: adapter.abandon_open_draft(chat, "partial answer"),
}


@pytest.mark.parametrize("door", sorted(_ADAPTER_DOORS))
@pytest.mark.parametrize("chat, calls", [("111", 1), ("222", 0)])
def test_adapter_content_door(door, chat, calls):
    from agent.outbound_lock import is_outbound_refusal

    _allow("telegram:111")
    adapter = _fake_telegram()
    result = asyncio.run(_ADAPTER_DOORS[door](adapter, chat))
    assert len(adapter.calls) == calls
    assert is_outbound_refusal(result) is not bool(calls)


def test_a_refused_task_card_does_not_fall_back_to_text():
    from gateway.relay.egress import declined_send

    _allow("telegram:111")
    result = asyncio.run(_fake_telegram().send_native_task_card_progress("222", [{"id": "t1"}]))
    assert declined_send(result)  # _task_card_publish suppresses its text fallback on a decline


def test_a_new_content_method_is_guarded_without_being_listed():
    from gateway.config import Platform, PlatformConfig
    from gateway.platforms.base import SendResult

    class WithPoll(type(_fake_telegram())):
        async def send_poll(self, chat_id, question, metadata=None) -> SendResult:
            self.calls.append(("poll", chat_id, question))
            return SendResult(success=True)

    _allow("telegram:111")
    adapter = WithPoll(PlatformConfig(enabled=True), Platform.TELEGRAM)
    adapter.calls = []
    refused = asyncio.run(adapter.send_poll("222", "lunch?"))
    asyncio.run(adapter.send_poll("111", "lunch?"))
    assert [call[1] for call in adapter.calls] == ["111"] and not refused.success


def test_an_adapter_without_its_own_clarify_still_has_no_separate_text_fallback():
    from gateway.run_turn_runner_clarify_delivery import text_fallback_coro

    coro = text_fallback_coro(_fake_telegram(), chat_id="111", question="which?", choices=None,
                              clarify_id="c1", session_key="s1")
    if coro is not None:
        coro.close()
    assert coro is None  # guarding the inherited send_clarify must keep it the base method


def _slack(reply_in_thread: bool):
    from gateway.config import PlatformConfig
    from plugins.platforms.slack.adapter import SlackAdapter

    adapter = SlackAdapter(PlatformConfig(enabled=True, extra={"reply_in_thread": reply_in_thread}))
    posted = []

    async def post(**kwargs):
        posted.append(kwargs)
        return {"ok": True, "ts": "1700.2"}

    client = AsyncMock()
    client.chat_postMessage.side_effect = post
    adapter._app = SimpleNamespace(client=client)
    return adapter, posted


@pytest.mark.parametrize("reply_in_thread, calls", [(True, 1), (False, 0)])
def test_slack_thread_entry_is_checked_on_the_thread_the_adapter_posts_to(reply_in_thread, calls):
    _allow("slack:C12345678:123.456")
    adapter, posted = _slack(reply_in_thread)
    # reply_in_thread=false drops a thread equal to reply_to: the reply would land at channel level.
    asyncio.run(adapter.send("C12345678", "hi", reply_to="123.456", metadata={"thread_id": "123.456"}))
    assert len(posted) == calls
    assert [p.get("thread_ts") for p in posted] == ["123.456"] * calls


@pytest.mark.parametrize("parent, calls", [("C12345678", 1), ("C99999999", 0)])
def test_slack_handoff_thread_seed(parent, calls):
    _allow("slack:C12345678")
    adapter, posted = _slack(True)
    thread = asyncio.run(adapter.create_handoff_thread(parent, "handoff"))
    assert len(posted) == calls and (thread is not None) is bool(calls)


@pytest.fixture
def profiles(tmp_path, monkeypatch):
    """Two named profiles under one root; the active one is ``alpha``."""
    from hermes_constants import reset_hermes_home_override, set_hermes_home_override

    root = tmp_path / ".hermes"
    homes = {name: root / "profiles" / name for name in ("alpha", "beta")}
    for home in homes.values():
        home.mkdir(parents=True)
        (home / "state.db").write_bytes(b"")
    monkeypatch.setattr(Path, "home", lambda: tmp_path)
    monkeypatch.setenv("HERMES_HOME", str(homes["alpha"]))
    token = set_hermes_home_override(homes["alpha"])
    yield homes
    reset_hermes_home_override(token)


@pytest.mark.parametrize("deliver, calls", [("bot-chat", 1), ("bot-chat:alpha", 1), ("bot-chat:beta", 0)])
def test_cron_bot_chat_to_its_own_profile_is_never_refused(profiles, deliver, calls):
    from cron.scheduler import _deliver_result

    _allow("telegram:111")
    config = SimpleNamespace(platforms={}, get_home_channel=lambda platform: None)
    bot_chat = MagicMock(return_value=None)
    with patch("gateway.config.load_gateway_config", return_value=config), \
         patch("cron.scheduler_delivery._deliver_to_bot_chat", new=bot_chat):
        error = _deliver_result({"id": "brief", "name": "brief", "deliver": deliver}, "report")
    assert bot_chat.call_count == calls
    assert (error is None) is bool(calls)


@pytest.mark.parametrize("allow, target, calls", [
    (("agent:beta",), "beta", 1), (("telegram:111",), "beta", 0), (("telegram:111",), "alpha", 1)])
def test_a_parked_bot_chat_record_is_rechecked_when_it_is_drained(profiles, allow, target, calls):
    from cron import bot_chat_legacy

    # Parked (or copied in) while the list allowed it; the list in force at drain time decides.
    bot_chat_legacy.defer("k1", {"id": "brief"}, "report", profiles[target], for_failure=False)
    _allow(*allow)
    deliver = MagicMock(return_value={"status": "settled"})
    with patch("tools.bot_live_delivery.find_canonical_live_owner", return_value=object()), \
         patch("tools.bot_live_delivery.deliver_to_live_owner", new=deliver):
        bot_chat_legacy.drain_legacy_pending()
    record = json.loads((profiles["alpha"] / "cron" / "bot_chat_pending" / "k1.json").read_text(encoding="utf-8"))
    assert deliver.call_count == calls
    assert record["status"] == ("transferred" if calls else "refused")


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
