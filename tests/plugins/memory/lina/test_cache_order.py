"""lina#21: a request is composed in cache order, so each request's bytes start with the previous one's.

Order: system and persona -> notes and person card (frozen per session) -> LCM-X's upper summary DAG -> recent
turns -> found memory as a tool result. A real ``AIAgent`` with ``context.engine: lcm-x`` and
``memory.provider: lina-memory`` talks to a loopback model that records every request body; turns are synthetic.
Only a compaction may rewrite earlier bytes, and the upper summary block stays pinned across one.
"""

from __future__ import annotations

import json
import re
import threading
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from types import SimpleNamespace
from unittest.mock import patch

import pytest

from agent.prompt_builder import NOTES_END, NOTES_HEADING, RUNTIME_ENVIRONMENT_END
from tests.plugins.memory.lina.harness import write_home

TURNS = 50
RECALL_TURN, SKILL_TURN, NEXT_DAY_TURN = 7, 12, 30
NOTE, CARD, RECALL = "NOTE-7731 the garden faces south", "CARD-7731 Alice grows tomatoes", "RECALL-7731 tomatoes need sun"
FILLER = "about soil and seeds " * 100
CARD_BUILDS: list[int] = []
_PART = re.compile(r"\[[\w ]+ Summary \(d(\d+), node \d+\)\]")


class RecordingModel(BaseHTTPRequestHandler):
    """An OpenAI-compatible model that records each request body and the engine's compaction count at that time."""

    def log_message(self, *args):
        pass

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        self.server.requests.append(SimpleNamespace(
            body=body, compactions=self.server.agent.context_compressor.compression_count))
        message = {"role": "assistant", "content": f"reply {len(self.server.requests)}"}
        if body.get("stream"):
            payload = ("data: " + json.dumps({"id": "r", "object": "chat.completion.chunk", "model": "m", "choices": [
                {"index": 0, "delta": message, "finish_reason": "stop"}]}) + "\n\ndata: [DONE]\n\n").encode()
            kind = "text/event-stream"
        else:
            payload = json.dumps({"id": "r", "object": "chat.completion", "model": "m", "choices": [
                {"index": 0, "message": message, "finish_reason": "stop"}],
                "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}}).encode()
            kind = "application/json"
        self.send_response(200)
        self.send_header("Content-Type", kind)
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


@contextmanager
def recording_model():
    server = ThreadingHTTPServer(("127.0.0.1", 0), RecordingModel)
    server.requests = []
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield server
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def _fake_summary_model(**kwargs):
    system = "\n".join(m["content"] for m in kwargs["messages"] if m["role"] == "system")
    nonce = re.search(r'<lcm-summary nonce="([0-9a-f]+)">', system).group(1)
    text = (f'<lcm-summary nonce="{nonce}">\nThe person asked numbered garden questions; each was answered.\n'
            "Expand for details about: the numbered garden questions\n</lcm-summary>")
    return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


class Clock:
    def __init__(self) -> None:
        self.now = datetime(2026, 11, 3, 9, 0, tzinfo=UTC)

    def __call__(self):
        return self.now


@pytest.fixture
def clock(monkeypatch):
    clock = Clock()
    monkeypatch.setattr("hermes_time.now", clock)
    return clock


@pytest.fixture
def home(tmp_path, monkeypatch, lina_env):
    home = write_home(tmp_path / ".hermes")
    (home / "memories").mkdir()
    (home / "memories" / "MEMORY.md").write_text(NOTE, encoding="utf-8")
    (home / "memories" / "USER.md").write_text(CARD, encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    # Small leaves, a short fresh tail and pairwise condensation give upper (d1) nodes within a few compactions.
    for key, value in (("LCM_LEAF_CHUNK_TOKENS", "3000"), ("LCM_CONDENSATION_FANIN", "2"),
                       ("LCM_FRESH_TAIL_COUNT", "8"), ("LCM_THRESHOLD_FULL_SWEEP_ENABLED", "false")):
        monkeypatch.setenv(key, value)
    from plugins.memory.lina import LinaMemoryProvider
    monkeypatch.setattr(LinaMemoryProvider, "prefetch",
                        lambda self, query, session_id="": f"- {RECALL}" if "recall" in query else "")
    builds = CARD_BUILDS
    builds.clear()

    def person_card(self):
        builds.append(len(builds) + 1)
        return f"HONCHO-CARD build {len(builds)}"

    monkeypatch.setattr(LinaMemoryProvider, "session_card_block", person_card)
    return home


def _configure(home, url: str, memory: str = "") -> None:
    config = (home / "config.yaml").read_text(encoding="utf-8").replace("memory:\n", "memory:\n" + memory)
    config += (f"model:\n  provider: custom\n  default: m\n  base_url: {url}\n  context_length: 64000\n"
               "lcm:\n  context_threshold: 0.25\nauxiliary:\n  title_generation:\n    enabled: false\n")
    (home / "config.yaml").write_text(config, encoding="utf-8")


def _agent(home, server, session_id: str, start: datetime):
    from hermes_state import SessionDB
    from run_agent import AIAgent

    # A toolset of only memory and skills reads to LCM-X as an auxiliary agent, which LCM-X never stores.
    agent = AIAgent(api_key="loopback", base_url=server.url, provider="custom", model="m", quiet_mode=True,
                    skip_context_files=True, session_id=session_id, platform="cli",
                    session_db=SessionDB(db_path=home / "state.db"), skip_background_review=True,
                    enabled_toolsets=["skills", "memory", "todo"])
    # These session ids carry no date stamp, so the prompt's "Conversation started:" date comes from
    # agent.session_start, minted from the real clock at init. Pin it to the test clock: otherwise the
    # first turn already tells a date other than the pinned one and the day never turns as the test says.
    agent.session_start = start
    server.agent = agent
    assert type(agent.context_compressor).__module__ == "plugins.context_engine.lcm-x.engine"
    return agent


def _add_skill(home) -> None:
    skill = home / "skills" / "garden" / "seed-calendar"
    skill.mkdir(parents=True)
    (skill / "SKILL.md").write_text("---\nname: seed-calendar\ndescription: When to sow each seed.\n---\n\nSow early.\n",
                                    encoding="utf-8")


def _converse(agent, turns: int, before_turn=lambda n: None) -> None:
    history = None
    with patch("agent.auxiliary_client.call_llm", side_effect=_fake_summary_model):
        for n in range(turns):
            before_turn(n)
            ask = "please recall the plan" if n == RECALL_TURN else "what next"
            history = agent.run_conversation(f"garden question {n}: {ask} {FILLER}", conversation_history=history)["messages"]


def _wire(request) -> list[str]:
    """The request as the provider prefix-matches it: tools, then each message in order."""
    return [json.dumps(request.body.get("tools"), sort_keys=True),
            *(json.dumps(m, sort_keys=True, ensure_ascii=False) for m in request.body["messages"])]


def _text(message) -> str:
    return json.dumps(message.get("content"), ensure_ascii=False)


def _summary_parts(request) -> list[tuple[int, str]]:
    """(depth, text) of each node rendered in the LCM-X summary message, in order."""
    content = next(m["content"] for m in request.body["messages"][1:] if _PART.search(str(m.get("content"))))
    return [(int(_PART.search(part).group(1)), part) for part in content.split("\n\n---\n\n") if _PART.search(part)]


def test_fifty_turns_each_request_starts_with_the_previous_one_except_across_a_compaction(home, clock):
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(home, server.url)
        agent = _agent(home, server, "sess-prefix", clock.now)

        def before_turn(n: int) -> None:
            if n == SKILL_TURN:
                _add_skill(home)
            if n == NEXT_DAY_TURN:
                clock.now += timedelta(days=1)

        _converse(agent, TURNS, before_turn)
        requests = list(server.requests)

    assert len(requests) == TURNS
    crossings = 0
    for n in range(TURNS - 1):
        before, after = requests[n], requests[n + 1]
        if after.compactions != before.compactions:
            crossings += 1
            continue
        assert _wire(after)[:len(_wire(before))] == _wire(before), f"request {n + 1} rewrote request {n}"
    assert crossings >= 2

    # Every tool call in the history names a tool the request defines; memory_recall is there from the start.
    for request in requests:
        defined = {t["function"]["name"] for t in request.body.get("tools") or []}
        called = {c["function"]["name"] for m in request.body["messages"] for c in m.get("tool_calls") or []}
        assert called <= defined, called - defined
    assert "memory_recall" in {t["function"]["name"] for t in requests[0].body["tools"]}

    # The Honcho person card is fetched only when the prompt is built and sits in it until the next build.
    cards = [re.search(r"HONCHO-CARD build \d+", r.body["messages"][0]["content"]).group(0) for r in requests]
    assert len(set(cards)) == len(CARD_BUILDS) == len({r.compactions for r in requests})
    assert all(a == b for a, b, x, y in zip(cards, cards[1:], requests, requests[1:]) if x.compactions == y.compactions)
    assert not any("HONCHO-CARD" in _text(m) for r in requests for m in r.body["messages"][1:])

    # Found memory is the turn's last row, a tool result, and never part of a user row or the prompt.
    recall_messages = requests[RECALL_TURN].body["messages"]
    call, result = recall_messages[-2], recall_messages[-1]
    assert [c["function"]["name"] for c in call["tool_calls"]] == ["memory_recall"]
    assert result["role"] == "tool" and result["tool_call_id"] == call["tool_calls"][0]["id"] and RECALL in result["content"]
    assert not any(RECALL in _text(m) for r in requests for m in r.body["messages"] if m["role"] in ("system", "user"))

    # The day and the skill set change mid-session as turn notes; the prompt keeps its bytes.
    systems = {r.body["messages"][0]["content"] for r in requests[:NEXT_DAY_TURN + 2]
               if r.compactions == requests[0].compactions}
    assert len(systems) == 1
    assert "seed-calendar" in _text(requests[SKILL_TURN].body["messages"][-1])
    assert "Today's date: Wednesday, November 04, 2026" in _text(requests[NEXT_DAY_TURN].body["messages"][-1])
    assert not any("Today's date" in _text(r.body["messages"][-1]) for r in requests[:NEXT_DAY_TURN])

    # System and persona first, the notes and person card close the prompt, the date is not in it.
    system = requests[-1].body["messages"][0]["content"]
    assert (system.index(RUNTIME_ENVIRONMENT_END) < system.index(NOTES_HEADING) < system.index(NOTE)
            < system.index(CARD) < system.index("HONCHO-CARD"))
    assert system.endswith(NOTES_END) and "Today's date" not in system and "Conversation started: Tuesday, November 03" in system


def test_upper_summary_block_keeps_its_bytes_across_the_next_compaction(home, clock, monkeypatch):
    from plugins.memory.lina import LinaMemoryProvider

    # A card refetched at a compaction may differ; here it does not, so the cached prefix can reach the upper block.
    monkeypatch.setattr(LinaMemoryProvider, "session_card_block", lambda self: "HONCHO-CARD unchanged")
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(home, server.url)
        agent = _agent(home, server, "sess-pin", clock.now)
        compactions = agent.context_compressor

        def before_turn(n: int) -> None:
            # The day turns between the compaction that forms the upper block and the next one.
            if compactions.compression_count == 2 and clock.now.day == 3:
                clock.now += timedelta(days=1)

        _converse(agent, TURNS, before_turn)
        requests = list(server.requests)

    first_after = {}
    for request in requests:
        first_after.setdefault(request.compactions, request)
    assert {2, 3} <= set(first_after), sorted(first_after)
    formed, next_one = first_after[2], first_after[3]
    upper = [part for depth, part in _summary_parts(formed) if depth >= 1]
    assert upper, "no upper node formed"
    assert [part for depth, part in _summary_parts(next_one) if depth >= 1] == upper
    assert _summary_parts(next_one) != _summary_parts(formed)  # the compaction did add lower nodes
    # The cached prefix runs through the upper block: same prompt, and the summary row opens with the same bytes.
    assert next_one.body["messages"][0] == formed.body["messages"][0]
    upper_bytes = "\n\n---\n\n".join(upper)
    assert formed.body["messages"][1]["content"].startswith(upper_bytes)
    assert next_one.body["messages"][1]["content"].startswith(upper_bytes)


def test_auto_recall_append_setting_splices_recall_into_the_user_message(home, clock):
    with recording_model() as server:
        server.url = f"http://127.0.0.1:{server.server_port}/v1"
        _configure(home, server.url, "  auto_recall_append: true\n")
        agent = _agent(home, server, "sess-append", clock.now)
        _converse(agent, RECALL_TURN + 1)
        messages = server.requests[-1].body["messages"]

    assert messages[-1]["role"] == "user" and RECALL in messages[-1]["content"]
    assert not any(m.get("tool_calls") for m in messages)
