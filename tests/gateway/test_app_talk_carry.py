"""The LINA app's exchange reaches only the agent's next DM turn on a messaging platform (lina#167)."""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from gateway.app_talk_carry import carry_into_dm_turn, write_carry
from gateway.config import Platform
from gateway.session import SessionSource

NOTE = {"item": "T1", "title": "기기 1 점검", "message": "파랑", "reply": "알겠어요"}


def _runner(home):
    return SimpleNamespace(_resolve_profile_home_for_source=lambda source: home)


@pytest.mark.parametrize("platform, chat_type", [
    (Platform.TELEGRAM, "group"), (Platform.API_SERVER, "dm"), (Platform.LOCAL, "dm"), (Platform.WEBHOOK, "dm")])
def test_a_group_or_non_messaging_turn_leaves_the_note_waiting(tmp_path, platform, chat_type):
    write_carry(tmp_path, NOTE)
    source = SessionSource(platform=platform, user_id="1", chat_id="1", chat_type=chat_type)
    assert carry_into_dm_turn(_runner(tmp_path), source, "hi") == "hi"
    dm = SessionSource(platform=Platform.TELEGRAM, user_id="1", chat_id="1", chat_type="dm")
    assert "Owner: 파랑" in carry_into_dm_turn(_runner(tmp_path), dm, "hi")


def test_notes_arrive_oldest_first_and_once(tmp_path):
    write_carry(tmp_path, NOTE)
    write_carry(tmp_path, {**NOTE, "message": "빨강"})
    dm = SessionSource(platform=Platform.TELEGRAM, user_id="1", chat_id="1", chat_type="dm")
    first = carry_into_dm_turn(_runner(tmp_path), dm, "hi")
    assert first.index("Owner: 파랑") < first.index("Owner: 빨강") and first.endswith("\n\nhi")
    assert carry_into_dm_turn(_runner(tmp_path), dm, "again") == "again"
