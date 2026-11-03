"""Next-command notices and write-time state refresh (lina#36)."""

import json

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    env.goal_and_todos(check="evidence:결과 보고서")
    return env


@pytest.mark.parametrize("text", ["자료를 더 읽었다", "결과를 확인했다", "끝냈다"])
def test_note_names_confirmation_when_evidence_exists(work, text):
    # Given
    work.ok("evidence", "T1", "omo:OT-RESULT")
    # When
    output = work.ok("note", "T1", text)
    # Then
    assert output.count('work confirm T1 --by alpha --seen "<직접 확인한 것>"') == 1
    assert next(item for item in work.json()["items"] if item["id"] == "T1")["state"] == "open"


def test_note_has_no_confirmation_hint_when_evidence_is_missing(work):
    # Given / When
    output = work.ok("note", "T1", "자료를 읽었다")
    # Then
    assert "work confirm" not in output


@pytest.mark.parametrize("verb,args,status", [
    ("wait", ["--on", "주인 확인", "--next", "+1d"], "waiting"),
    ("evidence", ["omo:OT-RESULT"], "evidence_wait"),
    ("note", ["자료를 읽었다"], "evidence_wait"),
])
def test_state_refreshes_when_item_is_written(work, verb, args, status):
    # Given
    if verb == "note":
        work.ok("evidence", "T1", "omo:OT-RESULT")
    # When
    work.ok(verb, "T1", *args)
    # Then
    state = json.loads((work.work / "state.json").read_text(encoding="utf-8-sig"))
    assert state["items"]["T1"]["status"] == status
    assert state["items"]["T1"]["checked"] == NOW


def test_notice_names_next_command_when_evidence_needs_confirmation(work):
    # Given
    work.ok("evidence", "T1", "omo:OT-RESULT")
    # When
    work.ok("check", "T1", "--notify", "--quiet")
    # Then
    notice = work.notices("alpha")
    assert "다음:" in notice
    assert "work confirm T1 --by alpha --seen" in notice
    assert "멈춤" not in notice
