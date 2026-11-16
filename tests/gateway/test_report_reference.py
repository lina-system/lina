"""A chat message's reference to a report (lina#263, ``gateway/report_reference.py``): on a platform without
report cards (Telegram) the reference goes out as the summary line plus the page link, and the report's text never
reaches the chat; an agent turn with more to say than a few short messages leaves one report and one short message.
"""

from __future__ import annotations

import asyncio
import importlib
import json
from contextlib import closing
from types import SimpleNamespace
from unittest.mock import AsyncMock, MagicMock

import pytest

import hermes_state_reports as reports
from gateway.config import PlatformConfig
from gateway.report_reference import line, plain, references
from hermes_cli import lina_deps
from plugins.platforms.telegram.adapter import TelegramAdapter
from tools.registry import registry

PAGE = "https://hub.example.ts.net/reports/rep_0123456789ab/page"


def test_a_reference_line_reads_back_and_renders_as_the_summary_and_the_link():
    ref = line("rep_0123456789ab", PAGE, "Costs fell | 12%\nthis week")
    assert references(f"Done.\n{ref}\n") == [("rep_0123456789ab", PAGE, "Costs fell | 12% this week")]
    assert plain(f"Done.\n{ref}") == f"Done.\nCosts fell | 12% this week\n{PAGE}"
    assert plain("no reference [[report:rep_x|y|z]] inline") == "no reference [[report:rep_x|y|z]] inline"


def _telegram() -> TelegramAdapter:
    adapter = TelegramAdapter(PlatformConfig(enabled=True, token="t", extra={}))
    adapter._bot, adapter._app = AsyncMock(), MagicMock()
    adapter._bot.send_message = AsyncMock(return_value=SimpleNamespace(message_id=7))
    adapter._bot.edit_message_text = AsyncMock(return_value=SimpleNamespace(message_id=7))
    return adapter


def _shown(text: str) -> str:
    return text.replace("\\", "")


def test_telegram_sends_and_edits_a_reference_as_the_summary_line_and_the_page_link():
    adapter = _telegram()
    reply = f"Here it is.\n{line('rep_0123456789ab', PAGE, 'Costs fell 12% this week.')}"

    assert asyncio.run(adapter.send("123", reply)).success
    sent = _shown(adapter._bot.send_message.await_args.kwargs["text"])
    assert sent.splitlines() == ["Here it is.", "Costs fell 12% this week.", PAGE]

    asyncio.run(adapter.edit_message("123", "7", reply, finalize=True))
    edited = _shown(adapter._bot.edit_message_text.await_args.kwargs["text"])
    assert "[[report:" not in edited and edited.splitlines()[-1] == PAGE


@pytest.fixture
def lina_root(tmp_path, monkeypatch):
    importlib.import_module("tools.report_tool")
    monkeypatch.setattr(lina_deps, "lina_root", lambda: tmp_path / "lina")
    monkeypatch.setenv("HERMES_DASHBOARD_PUBLIC_URL", "https://hub.example.ts.net")


def test_a_turn_with_more_than_three_short_messages_to_say_leaves_one_report_and_one_short_message(lina_root):
    findings = [f"Finding {n}: disk {n} is at {80 + n}% and grows 2% a day." for n in range(1, 13)]
    result = json.loads(registry.dispatch("report", {
        "title": "Disk review", "summary": "Twelve disks checked; three need a swap this week.",
        "body": "\n".join(f"- {f}" for f in findings)}, session_id="s-1"))
    reply = f"Checked all disks.\n{result['reference']}"
    adapter = _telegram()

    asyncio.run(adapter.send("123", reply))

    assert adapter._bot.send_message.await_count == 1
    sent = _shown(adapter._bot.send_message.await_args.kwargs["text"])
    assert sent.splitlines() == ["Checked all disks.", "Twelve disks checked; three need a swap this week.",
                                 result["page"]]
    assert not any(f in sent for f in findings)
    with closing(reports.owner_connection()) as conn:
        assert [r.report_id for r in reports.listing(conn)] == [result["id"]]
