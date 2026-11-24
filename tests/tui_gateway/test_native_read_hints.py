"""Observe subdirectory hints on the next real compute-host model request."""

from __future__ import annotations

import json
from collections.abc import Callable
from pathlib import Path

import pytest

from tests.tui_gateway._turn_host_parity_support import SID, Reply, gateway, loopback_model
from tests.tui_gateway.test_rust_host_rollback import FIXTURE_SECRETS, home, rust_binary, store_dsn, workdir
from tests.tui_gateway.test_turn_host_parity import _config, _event, _host_pid, _tool_call

__all__ = ["home", "rust_binary", "store_dsn", "workdir"]


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("impl", ["python", "rust"])
def test_read_hints_follow_completed_tool_results(
    impl: str, workdir: Path, home: Path, rust_binary: Path, store_dsn: str,
    record_property: Callable[[str, str], None],
) -> None:
    cursor_tail = "End of cursor hint fixture."
    cursor_middle = "Middle of cursor hint fixture."
    fixtures = (
        ("success", "AGENTS.md", "Read hint fixture one."),
        ("failure", "CLAUDE.md", "Read hint fixture two."),
        ("cursor", ".cursorrules", "Read hint fixture three."),
    )
    for directory, filename, hint in fixtures:
        (workdir / directory).mkdir()
        if directory == "cursor":
            hint = f"{hint}\n{'x' * 32_000}\n{cursor_middle}\n{'y' * 32_000}\n{cursor_tail}"
        (workdir / directory / filename).write_text(hint, encoding="utf-8")
    (workdir / "success/sample.txt").write_text("Fixture file contents.\n", encoding="utf-8")
    (workdir / "cursor/sample.txt").write_text("Cursor file contents.\nSecond line.\n", encoding="utf-8")
    paths = ("success/sample.txt", "failure/missing.txt", "cursor/sample.txt", "success/sample.txt")
    calls = [_tool_call(index, path) for index, path in enumerate(paths, 1)]
    calls[2] = _tool_call(3, paths[2], limit=1)
    script = tuple(Reply(calls=(call,)) for call in calls) + (Reply(text="Finished."),)

    with loopback_model(script) as model:
        _config(home, workdir, model.url, impl)
        with gateway(home, rust_binary, {}, store_dsn, FIXTURE_SECRETS) as client:
            assert client.rpc("session.create", cwd=str(workdir))["session_id"] == SID
            assert client.rpc("prompt.submit", session_id=SID, text="Read the fixture paths.")["turn_isolation"]
            _host_pid(home, impl, rust_binary)
            assert _event(client, "message.complete")["payload"]["text"] == "Finished."

    assert not model.errors
    assert len(model.requests) == len(script)
    rows = [request["messages"][-1] for request in model.requests[1:]]
    assert [row["tool_call_id"] for row in rows] == [call["id"] for call in calls]
    record_property("next_request_tool_rows", json.dumps(rows, ensure_ascii=False))
    assert "Fixture file contents." in rows[0]["content"]
    assert "File not found:" in rows[1]["content"]
    assert "Cursor file contents." in rows[2]["content"]
    for row, (directory, filename, hint) in zip(rows[:3], fixtures, strict=True):
        assert f"{directory}/{filename}]\n{hint}" in row["content"]
    file_page, _, cursor_hint = rows[2]["content"].partition("\n\n[Subdirectory context discovered:")
    assert json.loads(file_page)["truncated"] is True
    assert "Second line." not in file_page
    assert "[...truncated .cursorrules:" in cursor_hint
    assert cursor_hint.endswith(cursor_tail)
    assert cursor_middle not in cursor_hint
    assert all(hint not in rows[3]["content"] for _, _, hint in fixtures)
