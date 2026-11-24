"""Verify repeated reasoning-only exhaustion with the real backoff."""

from pathlib import Path

import pytest

from tests.tui_gateway._rust_host_empty_support import REASONING, assert_empty_recovery_parity

pytestmark = pytest.mark.platforms("linux")


def test_repeated_reasoning_exhaustion_matches_requests_and_transcript(
    tmp_path: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    assert_empty_recovery_parity("reasoning-exhausts", (REASONING,) * 6, 6, tmp_path, monkeypatch)
