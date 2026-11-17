"""The memory question set distinguishes original evidence from a prior answer."""

import json
from pathlib import Path
from types import SimpleNamespace

from hermes_cli.memory_eval_search import load_questions
from plugins.memory.lina import grade
from plugins.memory.lina.search import MemorySearch, Readers, handle_tool_call
from tests.plugins.memory.lina.test_recall_origin import _Store


def test_prior_answer_is_not_evidence_for_the_memory_question():
    fixtures = Path(__file__).resolve().parents[3] / "fixtures" / "memory_eval"
    question = next(item for item in load_questions(fixtures / "questions.jsonl") if item.id == "a13")
    corpus = [json.loads(line) for line in (fixtures / "corpus.jsonl").read_text(encoding="utf-8").splitlines()
              if line.strip()]
    records = [item for item in corpus if item["ref"].startswith("agent:default:eval-evidence-")]
    store = _Store()
    store.rows = [
        SimpleNamespace(uid=item["ref"], source=item["source"], content=item["text"],
                        at="2026-11-01T00:00:00+00:00" if item["role"] == "assistant" else "2020-01-01T00:00:00+00:00",
                        speaker="agent" if item["role"] == "assistant" else "owner",
                        speaker_ref=None, author=item["role"],
                        grade="agent" if item["role"] == "assistant" else "owner")
        for item in sorted(records, key=lambda item: item["role"])
    ]
    search = MemorySearch(Readers(store=lambda: store))

    found = json.loads(handle_tool_call(
        search, "memory_search", {"query": question.question, "sources": [question.source], "limit": 2}))["results"]

    assert found[0]["ref"] == f"msg:{question.expected[0]}"
    prior_answer = next(hit for hit in found if hit["speaker"] == "agent")
    assert prior_answer["evidence"] is False
    assert prior_answer["origin"] == "agent_answer"
    assert "you said before; not evidence" in grade.recall_text([prior_answer])
