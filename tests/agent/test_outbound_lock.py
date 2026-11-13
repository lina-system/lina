"""``outbound.allow`` (lina#638): a test copy of an agent may message only the listed destinations.

Each test writes the active profile's ``config.yaml`` and drives a real door with a fake transport.
"""

from __future__ import annotations

import asyncio
import io
import json
from contextlib import redirect_stderr
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import AsyncMock, patch

import pytest

from agent import outbound_lock
from agent.outbound_lock import check_outbound, read_refusals
from hermes_constants import get_hermes_home


def _allow(*entries: str) -> None:
    config = get_hermes_home() / "config.yaml"
    config.parent.mkdir(parents=True, exist_ok=True)
    config.write_text(json.dumps({"outbound": {"allow": list(entries)}}), encoding="utf-8")  # JSON is YAML


def _fake_adapter(platform=None):
    from gateway.config import Platform, PlatformConfig
    from gateway.platforms.base import BasePlatformAdapter, SendResult

    class FakeTelegram(BasePlatformAdapter):
        async def connect(self, *, is_reconnect: bool = False) -> bool:
            return True

        async def disconnect(self) -> None:
            return None

        async def send(self, chat_id, content, reply_to=None, metadata=None):
            self.sent.append((chat_id, content))
            return SendResult(success=True, message_id="m1")

        async def get_chat_info(self, chat_id):
            return {}

    adapter = FakeTelegram(PlatformConfig(enabled=True), platform or Platform.TELEGRAM)
    adapter.sent = []
    return adapter


def test_without_the_key_every_destination_is_allowed():
    assert check_outbound("telegram", "222", door="test") is None
    assert check_outbound("agent", "anyone", door="test") is None
    assert read_refusals()["total"] == 0


def test_allowed_chat_passes_and_another_chat_on_the_same_platform_is_refused():
    _allow("telegram:111")
    assert check_outbound("telegram", "111", door="test") is None
    refusal = check_outbound("telegram", "222", door="test")
    assert refusal and "outbound.allow" in refusal and "telegram:222" in refusal
    assert check_outbound("discord", "111", door="test")
    assert read_refusals()["targets"]["telegram:222"]["count"] == 1


def test_a_thread_entry_allows_only_that_thread():
    _allow("telegram:111:7")
    assert check_outbound("telegram", "111", "7", door="test") is None
    assert check_outbound("telegram", "111", "8", door="test")
    assert check_outbound("telegram", "111", door="test")


def test_agent_entries_name_another_agent_or_every_agent_of_a_peer():
    _allow("agent:scout", "agent:lab/alice")
    assert check_outbound("agent", "scout", door="test") is None
    assert check_outbound("agent", "scout/any", door="test") is None
    assert check_outbound("agent", "lab/alice", door="test") is None
    assert check_outbound("agent", "lab/bob", door="test")
    assert check_outbound("agent", "other", door="test")


def test_a_malformed_value_allows_nothing():
    config = get_hermes_home() / "config.yaml"
    config.write_text(json.dumps({"outbound": {"allow": {"telegram": "111"}}}), encoding="utf-8")
    assert check_outbound("telegram", "111", door="test")


def test_the_list_is_reread_after_a_config_change_without_a_restart():
    _allow("telegram:111")
    assert check_outbound("telegram", "222", door="test")
    _allow("telegram:111", "telegram:222")
    assert check_outbound("telegram", "222", door="test") is None
    _allow()
    assert check_outbound("telegram", "333", door="test") is None


def test_a_refusal_is_logged_once_per_target_and_counted_every_time(caplog):
    _allow("telegram:111")
    outbound_lock._logged.clear()
    with caplog.at_level("WARNING", logger="agent.outbound_lock"):
        for _ in range(3):
            check_outbound("telegram", "222", door="test")
    assert sum("telegram:222" in r.getMessage() for r in caplog.records) == 1
    assert read_refusals()["total"] == 3


def test_gateway_adapter_send_is_refused_before_it_leaves_and_not_retried():
    _allow("telegram:111")
    adapter = _fake_adapter()
    ok = asyncio.run(adapter.send("111", "hello"))
    refused = asyncio.run(adapter._send_with_retry("222", "hello"))
    assert ok.success and adapter.sent == [("111", "hello")]
    assert not refused.success and "outbound.allow" in refused.error
    assert read_refusals()["total"] == 1


def test_the_local_app_stream_answers_its_own_client_whatever_the_list_says():
    from gateway.config import Platform

    _allow("telegram:111")
    adapter = _fake_adapter(Platform.LOCAL)
    assert asyncio.run(adapter.send("session-1", "hello")).success
    assert read_refusals()["total"] == 0


def test_send_message_refuses_another_chat_before_the_platform_sender():
    from gateway.config import Platform, PlatformConfig
    from tools.send_message_tool import _send_to_platform

    _allow("telegram:111")
    sender = AsyncMock(return_value={"success": True})
    with patch("tools.send_message_tool._send_telegram", new=sender):
        allowed = asyncio.run(_send_to_platform(Platform.TELEGRAM, PlatformConfig(enabled=True, token="t"), "111", "hi"))
        refused = asyncio.run(_send_to_platform(Platform.TELEGRAM, PlatformConfig(enabled=True, token="t"), "222", "hi"))
    assert allowed == {"success": True}
    assert "outbound.allow" in refused["error"]
    assert [call.args[1] for call in sender.await_args_list] == ["111"]


def _cron_job(deliver: str) -> dict:
    return {"id": "brief", "name": "brief", "deliver": deliver}


@pytest.mark.parametrize("home_channel, delivered", [("111", True), ("222", False)])
def test_cron_home_channel_alias_is_checked_after_resolution(monkeypatch, home_channel, delivered):
    from gateway.config import Platform, PlatformConfig
    from cron.scheduler import _deliver_result

    _allow("telegram:111")
    monkeypatch.setenv("TELEGRAM_HOME_CHANNEL", home_channel)
    config = SimpleNamespace(platforms={Platform.TELEGRAM: PlatformConfig(enabled=True, token="t")},
                             get_home_channel=lambda platform: None)
    send = AsyncMock(return_value={"success": True, "message_id": "1"})
    job = _cron_job("telegram")
    with patch("gateway.config.load_gateway_config", return_value=config), \
         patch("tools.send_message_tool._send_to_platform", new=send):
        error = _deliver_result(job, "report")
    assert send.await_count == (1 if delivered else 0)
    if delivered:
        assert error is None and job.get("last_delivery_refused") is None
    else:
        assert "outbound.allow" in error
        assert job["last_delivery_refused"] == ["telegram:222"]


def test_peer_dm_to_an_unlisted_agent_is_refused_before_any_request():
    from hermes_cli.subcommands import peer

    _allow("agent:friend")
    stderr = io.StringIO()
    with patch.object(peer, "_ensure_bot_chat", side_effect=AssertionError("request made")), \
         redirect_stderr(stderr):
        code = peer._peer_dm(SimpleNamespace(json=False), "hi", "stranger", None, "http://peer", "k")
    assert code == 1 and "outbound.allow" in stderr.getvalue()


def test_worker_end_notice_to_an_unlisted_agent_is_not_sent(tmp_path: Path):
    from hermes_cli.worker.notice import deliver

    _allow("agent:lead")
    job = {"id": "t1", "bot": "someone-else"}
    sent = deliver(job, "done", "end", tmp_path, ["false"])
    assert not sent.ok and "outbound.allow" in sent.how
    assert deliver({"id": "t2", "bot": "lead"}, "done", "end", tmp_path, ["true"]).ok


def test_doctor_reports_the_refusal_count(monkeypatch, capsys):
    from hermes_cli import doctor
    from hermes_cli.doctor_state import _check_outbound_lock

    monkeypatch.setattr(doctor, "HERMES_HOME", get_hermes_home())
    _allow("telegram:111")
    check_outbound("telegram", "222", door="test")
    _check_outbound_lock(False)
    out = capsys.readouterr().out
    assert "outbound.allow" in out and "refused 1 send" in out and "telegram:222" in out
    assert json.loads((get_hermes_home() / outbound_lock.REFUSALS_FILE).read_text())["total"] == 1
