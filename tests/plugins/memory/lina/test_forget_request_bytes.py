"""Wire regression for ordinary owner input and a visitor's forget-shaped input."""

from datetime import UTC, datetime
from io import BytesIO
from pathlib import Path
from typing import Final

import pytest

from tests.plugins.memory.lina import test_cache_order
from tests.plugins.memory.lina.test_cache_order import RecordingModel
from tests.plugins.memory.lina.test_taste_cache_order import seed_taste
from tests.plugins.memory.lina.test_turn_taste_routes import _route_agent

home = test_cache_order.home
START: Final = datetime(2026, 11, 21, 9, 0, tzinfo=UTC)
TASTE: Final = "워터컬러"


@pytest.mark.parametrize(
    "case,platform,user",
    [
        pytest.param("owner-empty", "cli", "Please outline a courtyard garden.", id="owner-empty"),
        pytest.param("nonowner-forget", "telegram", f"{TASTE} 취향 잊어줘", id="nonowner-forget"),
    ],
)
def test_ordinary_model_request_preserves_input(home, tmp_path, monkeypatch, memory_store_sql, case, platform, user):
    owner = platform == "cli"
    if not owner:
        seed_taste(memory_store_sql, TASTE, "active")
    initial_tastes = [] if owner else [(TASTE, "active")]
    tastes_sql = "SELECT label, status FROM memory.tastes"
    assert memory_store_sql(tastes_sql) == initial_tastes

    # Fix the prompt's host inputs before building it so the actual HTTP body
    # can be compared across checkouts without removing or rewriting fields.
    monkeypatch.setenv("NO_PROXY", "localhost,127.0.0.1,::1")
    monkeypatch.setattr("hermes_time.now", lambda: START)
    monkeypatch.setattr("agent.system_prompt.get_default_hermes_root", lambda: Path("/fixture/profile"))
    monkeypatch.setattr(
        "agent.prompt_builder.build_environment_hints", lambda: "Current working directory: /fixture/garden"
    )
    monkeypatch.setattr("agent.coding_context.build_coding_workspace_block", lambda cwd=None: "")
    monkeypatch.setattr("tools.env_probe.get_environment_probe_line", lambda: "")
    bodies: list[bytes] = []

    class WireRecordingModel(RecordingModel):
        def do_POST(self) -> None:
            stream = self.rfile
            raw = stream.read(int(self.headers["Content-Length"]))
            bodies.append(raw)
            with BytesIO(raw) as body:
                self.rfile = body
                try:
                    super().do_POST()
                finally:
                    self.rfile = stream

    monkeypatch.setattr(test_cache_order, "RecordingModel", WireRecordingModel)
    with _route_agent(home, platform) as (agent, requests):
        agent.session_start = START
        result = agent.run_conversation(user, turn_author=None if owner else {"id": "visitor"})
        assert agent._owner_taste_allowed is owner

    assert result["completed"] and result["final_response"] == "reply 1"
    (raw,) = bodies
    (tmp_path / f"{case}.body").write_bytes(raw)
    (request,) = requests
    system, current = request.body["messages"]
    assert system["role"] == "system" and system["content"]
    assert current == {"role": "user", "content": user}
    assert "memory_recall" in {tool["function"]["name"] for tool in request.body["tools"]}
    assert memory_store_sql(tastes_sql) == initial_tastes
