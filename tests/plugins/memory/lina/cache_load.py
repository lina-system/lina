"""lina#25: a 200-turn synthetic load through the real ``AIAgent`` against a model that caches like Anthropic.

``context.engine: lcm-x`` (its default compaction settings) and ``memory.provider: lina-memory``, recalls every ten
turns, a skill added, the date turning at midnight and two compactions. Every request becomes an OCX usage row
(``cache_server.py``) and, with ``memory.prefix_fingerprints`` on, a prefix fingerprint; ``lina memory cache-report``
reads both. Each run writes its rows and report beside it (``--basetemp`` keeps them). One test file per
``memory.auto_recall_append`` setting, so each stays well within the runner's per-file limit.
"""

from __future__ import annotations

import json
import re
import shutil
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from unittest.mock import patch

from hermes_cli import cache_measure
from tests.plugins.memory.lina.cache_server import cache_model
from tests.plugins.memory.lina.harness import write_home

TURNS = 200
RECALL_EVERY, SKILL_TURN = 10, 40
START, STEP = datetime(2026, 11, 3, 22, 0, tzinfo=UTC), timedelta(minutes=1)
RECALL = "RECALL-25 " + "the tomatoes on the south bed need sun and water every morning. " * 19
FILLER = "about soil and seeds " * 200
BASELINES = (98.10784, 98.29365, 98.26721)


@dataclass(frozen=True)
class Load:
    rows: list
    fingerprints: list
    compactions: list
    last_timestamp: str


class Clock:
    def __init__(self) -> None:
        self.now = START

    def __call__(self):
        return self.now


def _fake_summary_model(**kwargs):
    system = "\n".join(m["content"] for m in kwargs["messages"] if m["role"] == "system")
    nonce = re.search(r'<lcm-summary nonce="([0-9a-f]+)">', system).group(1)
    text = (f'<lcm-summary nonce="{nonce}">\nThe person asked numbered garden questions; each was answered.\n'
            "Expand for details about: the numbered garden questions\n</lcm-summary>")
    return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


def _home(tmp_path, monkeypatch):
    home = write_home(tmp_path / ".hermes")
    (home / "memories").mkdir()
    (home / "memories" / "MEMORY.md").write_text("NOTE-25 the garden faces south", encoding="utf-8")
    (home / "memories" / "USER.md").write_text("CARD-25 Alice grows tomatoes", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    from plugins.memory.lina import LinaMemoryProvider
    monkeypatch.setattr(LinaMemoryProvider, "prefetch",
                        lambda self, query, session_id="": f"- {RECALL}" if "recall" in query else "")
    monkeypatch.setattr(LinaMemoryProvider, "session_card_block", lambda self: "HONCHO-CARD unchanged", raising=False)
    return home


def _configure(home, url: str, memory: str) -> None:
    config = (home / "config.yaml").read_text(encoding="utf-8").replace("memory:\n", "memory:\n" + memory)
    config += (f"model:\n  provider: custom\n  default: m\n  base_url: {url}\n  context_length: 256000\n"
               "lcm:\n  context_threshold: 0.4\nauxiliary:\n  title_generation:\n    enabled: false\n")
    (home / "config.yaml").write_text(config, encoding="utf-8")


def _drive(home, server, clock) -> None:
    from hermes_state import SessionDB
    from run_agent import AIAgent

    agent = AIAgent(api_key="loopback", base_url=server.url, provider="custom", model="m", quiet_mode=True,
                    skip_context_files=True, session_id="sess-load", platform="cli",
                    session_db=SessionDB(db_path=home / "state.db"), skip_background_review=True,
                    enabled_toolsets=["skills", "memory", "todo"])
    # agent_init stamps the start from the real clock and "sess-load" carries no date stamp, so pin the
    # conversation start to the load's own clock; otherwise the midnight date note below never happens.
    agent.session_start = clock.now
    server.compaction_count = lambda: agent.context_compressor.compression_count
    history = None
    with patch("agent.auxiliary_client.call_llm", side_effect=_fake_summary_model):
        for n in range(TURNS):
            if n == SKILL_TURN:
                skill = home / "skills" / "garden" / "seed-calendar"
                skill.mkdir(parents=True)
                (skill / "SKILL.md").write_text(
                    "---\nname: seed-calendar\ndescription: When to sow each seed.\n---\n\nSow early.\n", encoding="utf-8")
            ask = "please recall the plan" if n % RECALL_EVERY == RECALL_EVERY // 2 else "what next"
            history = agent.run_conversation(f"garden question {n}: {ask} {FILLER}", conversation_history=history)["messages"]
            clock.now += STEP


def run_load(tmp_path, monkeypatch, *, append: bool) -> Load:
    home = _home(tmp_path, monkeypatch)
    clock = Clock()
    monkeypatch.setattr("hermes_time.now", clock)
    with cache_model(clock, "conv-load") as server:
        _configure(home, server.url, f"  prefix_fingerprints: true\n  auto_recall_append: {str(append).lower()}\n")
        _drive(home, server, clock)
        usage, compactions = list(server.usage), list(server.compactions)

    lines = [json.dumps(row) for row in usage]
    (tmp_path / "usage.jsonl").write_text("\n".join(lines) + "\n", encoding="utf-8")
    shutil.copy(home / "state" / "prefix-fingerprints.jsonl", tmp_path / "fingerprints.jsonl")
    fingerprints = cache_measure.load_fingerprints(
        (tmp_path / "fingerprints.jsonl").read_text(encoding="utf-8").splitlines())
    rows = cache_measure.load_usage(lines)
    text = cache_measure.report(rows, baselines=BASELINES, fingerprints=fingerprints)
    (tmp_path / "report.md").write_text(text, encoding="utf-8")
    print(f"\n[{'appended' if append else 'tool-result'}] compactions={compactions[-1]}\n{text}")
    return Load(rows, fingerprints, compactions, usage[-1]["timestamp"])
