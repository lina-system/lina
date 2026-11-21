"""Fixed answers to the rule questions, using the conversation tool's real scoped reads."""

import json
from pathlib import Path
from uuid import uuid4

import pytest

from plugins.memory.lina import LinaMemoryProvider
from tools.approval import request_tool_approval


QUESTIONS = json.loads(
    (Path(__file__).resolve().parents[3] / "fixtures/memory_eval/rule_questions.json").read_text(encoding="utf-8")
)


@pytest.fixture(params=["alpha", "beta"])
def reader(request):
    return request.param


@pytest.fixture
def peer(reader):
    return "beta" if reader == "alpha" else "alpha"


@pytest.fixture
def turn(memory_store_sql, tmp_path, monkeypatch):
    """Each call has a fresh session and persisted owner question, like the standing-rule tool tests."""
    def call(agent, room, words, action, *, date="2025-03-04", **args):
        uid = uuid4().hex
        provider = LinaMemoryProvider()
        provider._home = str(tmp_path / "profiles" / agent)
        provider._session_id = "session-" + uid
        assert "standing_rule" in {tool["name"] for tool in provider.get_tool_schemas()}
        memory_store_sql(
            """INSERT INTO memory.messages
               (uid, source, agent_id, session_id, role, speaker, content, content_sha256, conversation_id, ts)
               VALUES (%s, 'agent', %s, %s, 'user', 'owner', %s, 'fixture', %s, %s)""",
            (uid, agent, provider._session_id, words, room, date),
        )
        with monkeypatch.context() as patch:
            patch.setattr(LinaMemoryProvider, "_rule_source_uid", property(lambda self: uid), raising=False)
            result = json.loads(provider.handle_tool_call("standing_rule", {"action": action, **args}))
        assert "error" not in result, result
        return result

    return call


def answer(turn, agent, room, question):
    """The fixed model sees only the list returned to the answering agent at this place."""
    rules = turn(agent, room, question, "list")["rules"]
    assert all(row["status"] == "active" for row in rules), rules
    if not rules:
        return "Ready."
    priority = {"all": 0, "agent": 1, "place": 2}
    chosen = max(rules, key=lambda row: priority[row["scope"]])
    return {"answer in English": "The sample is ready.", "answer in Korean": "준비됐어요."}[chosen["statement"]]


@pytest.mark.after_774
def test_shared_rule_reaches_peer(reader, peer, turn):
    q = QUESTIONS["reach"]
    turn(peer, "studio", q["owner"], "record")
    assert answer(turn, reader, "archive", q["question"]) == q["expected"]


@pytest.mark.after_774
def test_private_rule_stays_with_recipient(reader, peer, turn):
    q = QUESTIONS["isolation"]
    assert answer(turn, reader, "studio", q["question"]) == q["other_agent_answer"]
    turn(peer, "studio", q["owner"], "record")
    assert answer(turn, peer, "studio", q["question"]) == q["owner_agent_answer"]
    assert answer(turn, reader, "studio", q["question"]) == q["other_agent_answer"]


@pytest.mark.after_774
def test_place_rule_wins_only_in_its_room(reader, peer, turn):
    q = QUESTIONS["precedence"]
    turn(peer, "studio", q["local"], "record")
    turn(peer, "archive", q["owner"], "record")
    assert answer(turn, reader, "studio", q["question"]) == q["in_room"]
    assert answer(turn, reader, "archive", q["question"]) == q["elsewhere"]


@pytest.mark.after_774
def test_revision_and_dated_previous_answer(reader, peer, turn):
    q = QUESTIONS["change"]
    saved = turn(peer, "studio", q["owner"], "record", date="2025-01-02")
    assert answer(turn, reader, "archive", q["question"]) == q["before"]
    turn(peer, "studio", q["revision"], "change", rule_id=saved["rule_id"], date="2025-02-03")
    assert answer(turn, reader, "archive", q["question"]) == q["after"]
    history = turn(reader, "archive", q["history_question"], "list", history=True)["rules"]
    old, = [rule for rule in history if rule["status"] == "replaced"]
    reply = f"{old['said_at'][:10]}: {old['statement']} (replaced {old['replaced_at'][:10]})."
    assert reply == q["history_answer"]


def test_approval_prompt_survives_rule_request(reader, turn, tmp_path, monkeypatch):
    q = QUESTIONS["approval"]
    home = tmp_path / "profiles" / reader
    home.mkdir(parents=True)
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("HERMES_INTERACTIVE", "1")

    def prompt():
        seen = []

        def deny(command, description, **options):
            seen.append((command, description, options))
            return q["expected"]

        decision = request_tool_approval("publish_sample", q["reason"], approval_callback=deny)
        assert decision["approved"] is False
        message, = seen
        assert message[1] == q["reason"]
        return message

    before = prompt()
    refusal = turn(reader, "studio", q["owner"], "record")
    assert "승인" in refusal["refusal"]
    assert turn(reader, "studio", q["question"], "list")["rules"] == []
    assert prompt() == before
