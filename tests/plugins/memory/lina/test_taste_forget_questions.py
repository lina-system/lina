"""Regression for #198: a correction governs future taste context and preserves raw history."""

from __future__ import annotations

import json
import re
from contextlib import ExitStack
from hashlib import sha256
from pathlib import Path
from types import SimpleNamespace
from typing import Final

import pytest
from pydantic import BaseModel, ConfigDict

from agent.turn_context_taste import strip_owner_taste_suffix
from tests.plugins.memory.lina.harness import drain, write_home
from tests.plugins.memory.lina.test_cache_order import (
    FILLER,
    TURNS,
    Clock,
    RecordingModel,
    _agent,
    _configure,
    _summary_parts,
    _wire,
    recording_model,
)
from tests.plugins.memory.lina.test_taste_cache_order import StoreSQL, seed_taste, self_note_and_frame


class CorrectionQuestion(BaseModel):
    model_config = ConfigDict(frozen=True)

    target: str
    alternative: str
    history: str
    lookup: str
    request: str
    question: str


_FIXTURE: Final = Path(__file__).resolve().parents[3] / "fixtures/memory_eval/taste_forget_questions.json"
QUESTIONS: Final = {
    name: CorrectionQuestion.model_validate(data)
    for name, data in json.loads(_FIXTURE.read_text(encoding="utf-8")).items()
}


class CorrectionModel(RecordingModel):
    """Record the assembled request and answer from its system frame and current recall."""

    def do_POST(self) -> None:
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.requests.append(SimpleNamespace(
            body=body, compactions=self.server.agent.context_compressor.compression_count,
        ))
        question = self.server.question
        messages = body["messages"]
        latest_user = max(i for i, row in enumerate(messages) if row["role"] == "user")
        tastes = "\n".join(
            row["content"] for row in [messages[0], *messages[latest_user + 1:]]
            if row["role"] in {"system", "tool"} and row.get("content")
        )
        picks = set(re.findall(r"^like: (.+)$", tastes, re.MULTILINE))
        choice = next((label for label in (question.target, question.alternative) if label in picks), "")
        answer = "Ready."
        if question.question in messages[latest_user]["content"]:
            answer = f"I suggest {choice}." if choice else "I have no saved suggestion."
        message = {"role": "assistant", "content": answer}
        if body.get("stream"):
            chunk = {"id": "fixture", "object": "chat.completion.chunk", "model": "m",
                     "choices": [{"index": 0, "delta": message, "finish_reason": "stop"}]}
            payload = f"data: {json.dumps(chunk)}\n\ndata: [DONE]\n\n"
            content_type = "text/event-stream"
        else:
            payload = json.dumps({
                "id": "fixture", "object": "chat.completion", "model": "m",
                "choices": [{"index": 0, "message": message, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
            })
            content_type = "application/json"
        encoded = payload.encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(encoded)))
        self.end_headers()
        self.wfile.write(encoded)


@pytest.fixture
def correction_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch, lina_env) -> Path:
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv("NO_PROXY", "127.0.0.1")
    monkeypatch.setenv("no_proxy", "127.0.0.1")
    monkeypatch.chdir(tmp_path)
    for key, value in (
        ("LCM_LEAF_CHUNK_TOKENS", "3000"),
        ("LCM_CONDENSATION_FANIN", "2"),
        ("LCM_FRESH_TAIL_COUNT", "8"),
        ("LCM_THRESHOLD_FULL_SWEEP_ENABLED", "false"),
    ):
        monkeypatch.setenv(key, value)
    return home


@pytest.fixture
def conversation(correction_home: Path, monkeypatch: pytest.MonkeyPatch):
    """Drive the regular agent, with deterministic answers at the two model boundaries."""
    def start(question: CorrectionQuestion):
        def summarize(**kwargs):
            messages = kwargs["messages"]
            system = "\n".join(row["content"] for row in messages if row["role"] == "system")
            nonce = re.search(r'<lcm-summary nonce="([0-9a-f]+)">', system)
            assert nonce is not None
            # Preserve eligible source text so the initial summary really contains the target.
            source = "\n".join(row["content"] for row in messages if row["role"] != "system")
            details = f"{question.history}\n" if question.history in source else ""
            text = (f'<lcm-summary nonce="{nonce[1]}">\n{details}'
                    "The exhibit planning continued. The participants discussed the current layout "
                    "and reviewed preparations for the public display.\n"
                    "Expand for details about: exhibit planning\n</lcm-summary>")
            return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])

        server = resources.enter_context(recording_model())
        server.RequestHandlerClass = CorrectionModel
        server.question = question
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(correction_home, server.url)
        monkeypatch.setattr("agent.auxiliary_client.call_llm", summarize)
        agent = _agent(correction_home, server, "taste-correction", Clock().now)
        resources.callback(agent.close)
        return agent, server.requests

    with ExitStack() as resources:
        yield start


def _forget(question: CorrectionQuestion, home: Path) -> None:
    from hermes_cli.heart.forget import forget_from_conversation

    result = forget_from_conversation(
        question.request, owner_verified=True, history=[question.history], target_home=home,
    )
    assert result is not None and result.forgotten is not None
    assert result.forgotten.label == question.target


def _prefix_sha(request, length: int) -> str:
    return sha256(json.dumps(_wire(request)[:length], ensure_ascii=False).encode("utf-8")).hexdigest()


def test_forgetting_active_taste_changes_answer_and_keeps_previous_raw_search(
    correction_home: Path, conversation, memory_store_sql: StoreSQL,
) -> None:
    question = QUESTIONS["active_pick"]
    seed_taste(memory_store_sql, question.target, "active")
    seed_taste(memory_store_sql, question.alternative, "active")
    agent, requests = conversation(question)
    source = agent.run_conversation(question.history)
    drain(agent)
    before = agent.run_conversation(question.question, conversation_history=source["messages"])
    original_request = requests[-1]
    assert question.target in before["final_response"]

    def raw_history() -> list[str]:
        result = json.loads(agent._memory_manager.handle_tool_call(
            "memory_search", {"query": question.lookup, "sources": ["agent"], "speaker": ["owner"]},
            session_id=agent.session_id,
        ))
        return [hit["ref"] for hit in result["results"]
                if hit["source"] == "agent" and hit["excerpt"] == question.history]

    prior_ref, = raw_history()
    _forget(question, correction_home)
    after = agent.run_conversation(question.question, conversation_history=before["messages"])
    corrected_request = requests[-1]

    assert question.alternative in after["final_response"]
    assert question.target not in after["final_response"]
    latest_recall = corrected_request.body["messages"][-1]
    assert latest_recall["role"] == "tool"
    content = latest_recall["content"]
    picks = content[len(strip_owner_taste_suffix(content)):]
    assert f"like: {question.alternative}" in picks
    assert question.target not in picks
    assert corrected_request.compactions == original_request.compactions
    prefix_length = len(_wire(original_request))
    assert _prefix_sha(corrected_request, prefix_length) == _prefix_sha(original_request, prefix_length)
    assert raw_history() == [prior_ref]


def test_forgetting_core_taste_keeps_frozen_prefix_until_summary_rebuild(
    correction_home: Path, conversation, memory_store_sql: StoreSQL,
) -> None:
    question = QUESTIONS["compacted_summary"]
    seed_taste(memory_store_sql, question.target, "core")
    seed_taste(memory_store_sql, question.alternative, "core")
    agent, requests = conversation(question)
    turn = agent.run_conversation(question.history)
    starting_count = requests[-1].compactions
    for index in range(TURNS):
        turn = agent.run_conversation(f"Exhibit planning {index}: {FILLER}", conversation_history=turn["messages"])
        if requests[-1].compactions > starting_count:
            break
    before = agent.run_conversation(question.question, conversation_history=turn["messages"])
    original = requests[-1]
    assert original.compactions > starting_count
    assert question.target in before["final_response"]
    assert any(question.target in text for _, text in _summary_parts(original))
    _, original_frame = self_note_and_frame(original.body["messages"][0]["content"])

    _forget(question, correction_home)
    frozen = agent.run_conversation(question.question, conversation_history=before["messages"])
    assert requests[-1].compactions == original.compactions
    prefix_length = len(_wire(original))
    assert _prefix_sha(requests[-1], prefix_length) == _prefix_sha(original, prefix_length)
    for index in range(TURNS):
        frozen = agent.run_conversation(f"Exhibit layout {index}: {FILLER}", conversation_history=frozen["messages"])
        if requests[-1].compactions > original.compactions:
            break
    after = agent.run_conversation(question.question, conversation_history=frozen["messages"])
    rebuilt = requests[-1]

    assert rebuilt.compactions > original.compactions
    rebuilt_note, rebuilt_frame = self_note_and_frame(rebuilt.body["messages"][0]["content"])
    assert sha256(rebuilt_frame.encode("utf-8")).digest() == sha256(original_frame.encode("utf-8")).digest()
    assert question.target not in rebuilt_note
    assert all(question.target not in text for _, text in _summary_parts(rebuilt))
    assert question.alternative in after["final_response"]
    assert question.target not in after["final_response"]
