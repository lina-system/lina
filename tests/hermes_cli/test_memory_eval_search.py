"""lina#19: the search quality check scores top-5 hits per source, old path against new."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path

import psycopg
import pytest

from hermes_cli.memory_eval_search import (
    EvalError,
    Question,
    load_questions,
    new_vector,
    report_lines,
    score,
)

FIXTURES = Path(__file__).resolve().parents[1] / "fixtures" / "memory_eval"


def _q(qid: str, source: str, expected: str) -> Question:
    return Question(id=qid, source=source, agent="default", question="?", expected=(expected,))


def test_a_hit_is_an_expected_ref_within_the_first_five():
    questions = [_q("a", "agent", "x"), _q("b", "agent", "y"), _q("m", "memory", "z")]
    new = {"a": ["1", "2", "3", "4", "x"], "b": ["1", "2", "3", "4", "5", "y"], "m": ["z"]}
    old = {"a": [], "b": ["y"], "m": None}
    scores = score(questions, old, new)
    assert (scores["agent"].questions, scores["agent"].new_hits, scores["agent"].old_hits) == (2, 1, 1)
    assert (scores["memory"].old_run, scores["memory"].old_rate, scores["memory"].new_rate) == (0, None, 1.0)
    lines = report_lines(scores)
    assert lines[1].split() == ["agent", "2", "50.0%", "50.0%"]
    assert lines[2].split() == ["memory", "1", "n/a", "100.0%"]


@pytest.mark.parametrize("line, message", [
    ('{"id": "q", "source": "lcm", "question": "?", "expected": ["r"]}', "not one of"),
    ('{"id": "q", "source": "agent", "question": "?", "expected": []}', "no expected refs"),
    ('{"id": "q", "source": "agent"}', "not a question line"),
])
def test_malformed_question_lines_are_refused_with_their_line_number(tmp_path, line, message):
    path = tmp_path / "q.jsonl"
    path.write_text(line + "\n", encoding="utf-8")
    with pytest.raises(EvalError, match=f"q.jsonl:1: .*{message}"):
        load_questions(path)


def test_the_synthetic_set_has_thirty_questions_over_all_sources_whose_refs_are_in_its_corpus():
    questions = load_questions(FIXTURES / "questions.jsonl")
    corpus = {json.loads(line)["ref"] for line in (FIXTURES / "corpus.jsonl").read_text(encoding="utf-8").splitlines()}
    assert len(questions) >= 30
    assert {q.source for q in questions} == {"agent", "messages", "memory"}
    assert all(set(q.expected) <= corpus for q in questions)


def _unit(axis: int) -> list[float]:
    vector = [0.0] * 1024
    vector[axis] = 1.0
    return vector


def test_the_new_path_ranks_every_source_of_the_agent_together(_memory_store, memory_store_sql):
    def message(uid, source, agent, content):
        return memory_store_sql(
            "INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256) VALUES "
            "(%s, %s, %s, 'user', %s, %s) RETURNING message_id",
            (uid, source, agent, content, hashlib.sha256(content.encode()).hexdigest()))[0][0]

    def vector(source, agent, message_id, axes):
        chunk = memory_store_sql("INSERT INTO vec.chunks (source, agent_id, message_ids, text) VALUES "
                                 "(%s, %s, %s, 't') RETURNING chunk_id", (source, agent, [message_id]))[0][0]
        literal = "[" + ",".join(str(v) for v in [sum(x) for x in zip(*[_unit(a) for a in axes])]) + "]"
        memory_store_sql("INSERT INTO vec.embeddings (kind, item_id, agent_id, model, dim, embedding) VALUES "
                         "('chunk', %s, %s, 'm', 1024, %s::vector)", (chunk, agent, literal))

    vector("agent", "default", message("agent:default:near", "agent", "default", "a"), [0])
    vector("messages", "default", message("messages:mid", "messages", "default", "b"), [0, 1])
    vector("agent", "other", message("agent:other:near", "agent", "other", "c"), [0])
    question = Question(id="q", source="agent", agent="default", question="?", expected=("agent:default:near",))
    with psycopg.connect(_memory_store.superuser_dsn(), autocommit=True) as conn:
        ranked = new_vector(conn, _unit(0), "m", question)
    assert ranked == ["agent:default:near", "messages:mid"]
