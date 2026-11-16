"""lina#380: a Korean agent's words ride found memory, so a 20-turn conversation keeps its prompt-cache hit rate.

A real ``AIAgent`` with ``display.language: ko`` and ``memory.provider: lina-memory`` talks to the loopback model that
caches like Anthropic (``cache_server.py``). The turns mix an unrelated question, a word of the owner's word list, an
English term he named in Korean and an English document. All text is synthetic.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime, timedelta

import pytest

from hermes_cli import cache_measure
from tests.plugins.memory.lina.cache_server import cache_model
from tests.plugins.memory.lina.harness import drain, write_home

pytest.importorskip("kiwipiepy", reason="Korean analysis needs the 'korean' extra (kiwipiepy), not installed here")

TURNS = 20
# The token-weighted hit rate of this fixture on origin/dev 937b42fce9, before lina#380.
BASELINE = 89.75047
ENGLISH_DOC = "The design doc says each taste slot holds one preference and the planner reads every slot. " * 6
QUESTIONS = (
    "오늘 정원에 물 줬어. 내일은 언제 줄까? {n}",
    "슬롯 정리해서 다시 보여줘 {n}",
    "taste slot 이야기 이어서 하자 {n}",
    ENGLISH_DOC * 8 + "{n}",
)
FILLER = " 흙과 씨앗 이야기" * 120


class Clock:
    def __init__(self) -> None:
        self.now = datetime(2026, 11, 16, 9, 0, tzinfo=UTC)

    def __call__(self):
        return self.now


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    monkeypatch.setenv("HERMES_HOME", str(home))
    return home


def _configure(home, url: str) -> None:
    config = (home / "config.yaml").read_text(encoding="utf-8")
    config += (f"display:\n  language: ko\nmodel:\n  provider: custom\n  default: m\n  base_url: {url}\n"
               "  context_length: 256000\nauxiliary:\n  title_generation:\n    enabled: false\n")
    (home / "config.yaml").write_text(config, encoding="utf-8")


def test_twenty_korean_turns_keep_the_cache_hit_rate(home, monkeypatch, memory_store_sql):
    from agent.i18n import reset_language_cache
    from hermes_cli.korean import words
    from hermes_state import SessionDB
    from run_agent import AIAgent

    with words.owner_connection() as conn:
        words.add(conn, "슬롯", "자리", "calque")
    clock = Clock()
    monkeypatch.setattr("hermes_time.now", clock)
    with cache_model(clock, "conv-korean") as server:
        _configure(home, server.url)
        reset_language_cache()
        db = SessionDB(db_path=home / "state.db")
        agent = AIAgent(api_key="loopback", base_url=server.url, provider="custom", model="m", quiet_mode=True,
                        skip_context_files=True, session_id="sess-korean", platform="cli", session_db=db,
                        skip_background_review=True, enabled_toolsets=["memory", "todo"])
        try:
            history = agent.run_conversation("taste slot은 취향 목록이라고 부르자" + FILLER)["messages"]
            drain(agent)
            for n in range(1, TURNS):
                clock.now += timedelta(minutes=1)
                question = QUESTIONS[n % len(QUESTIONS)].format(n=n)
                question += "" if question.startswith("The design doc") else FILLER
                history = agent.run_conversation(question, conversation_history=history)["messages"]
                drain(agent)
        finally:
            agent.shutdown_memory_provider()
            db.close()
        usage = list(server.usage)

    rate = cache_measure.rates(cache_measure.load_usage(json.dumps(row) for row in usage))
    assert rate.weighted is not None and rate.mean is not None
    print(f"\n[korean 20 turns] requests={rate.requests} weighted={rate.weighted:.5f} mean={rate.mean:.5f} "
          f"input_total={rate.input_total} cache_read={rate.cache_read}")
    assert rate.requests == TURNS
    assert rate.weighted >= BASELINE
