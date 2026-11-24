"""Check brief empty-reply recovery cases through both compute hosts."""

from pathlib import Path

import pytest

from tests.tui_gateway._rust_host_empty_support import (
    ANSWER,
    EMPTY,
    INLINE,
    REASONING,
    TOOL_CALL,
    assert_empty_recovery_parity,
)
from tests.tui_gateway._turn_host_parity_support import Json

pytestmark = pytest.mark.platforms("linux")


@pytest.mark.parametrize(
    ("attempts", "limit"),
    [
        pytest.param((EMPTY, {"content": ANSWER}), 2, id="empty-recovers"),
        pytest.param((REASONING, INLINE, {"content": ANSWER}), 3, id="reasoning-recovers"),
        pytest.param((EMPTY,) * 2, 2, id="empty-exhausts"),
        pytest.param(({"tool_calls": [TOOL_CALL]}, EMPTY, {"content": ANSWER}), 3, id="post-tool-recovers"),
        pytest.param(({"tool_calls": [TOOL_CALL]}, EMPTY, EMPTY, EMPTY), 4, id="post-tool-exhausts"),
        pytest.param(({"content_chunks": ["Use <thi", "nk> to start a block."]},), 1, id="literal-tag"),
    ],
)
def test_empty_recovery_requests_and_durable_transcript(
    attempts: tuple[Json | None, ...],
    limit: int,
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
    request: pytest.FixtureRequest,
) -> None:
    assert_empty_recovery_parity(request.node.callspec.id, attempts, limit, tmp_path, monkeypatch)
