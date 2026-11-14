"""The hub's daily health check of the runner sync (lina-system/lina#491): the serve tick runs
``lina runners sync status --issues`` and serves its lines as the ``runner-sync`` status check (lina#233).

The tick runs against a hub folder with the daily sync on, and the result is read the way the app reads it, through
``lina_status_checks.checks``.
"""

from __future__ import annotations

import pytest

from hermes_cli import lina_runner_sync_daily, lina_status_checks
from hermes_cli import lina_runner_sync_state as state
from tests.hermes_cli.test_lina_runner_sync import make_hub
from tests.hermes_cli.test_lina_runner_sync_node import _Ended


@pytest.fixture
def tick(tmp_path, monkeypatch):
    root = make_hub(tmp_path, monkeypatch)["root"]
    (root / "config.yaml").write_text('worker:\n  runner_sync: {at: "23:59"}\n', encoding="utf-8")
    monkeypatch.setattr(lina_runner_sync_daily, "_child", None)

    def run(now: float) -> list[dict]:
        lina_runner_sync_daily.maybe_start(root / "worker", now=now, spawn=lambda base, only, run: _Ended())
        return [row for row in lina_status_checks.checks(now)["checks"] if row["name"] == "runner-sync"]
    return root, run


def test_issue_lines_are_a_failing_check_that_names_them(tick):
    root, run = tick
    state.add_hold(root, "grok", "build-a", "1.0.71", "install failed", "node")
    state.write_json(state.daily_path(root), {"started": 1.0, "finished": 1.0, "rc": 1,
                                              "error": "could not start: OSError: no fork"})

    [row] = run(86400.0 * 400)

    assert (row["state"], row["ok"]) == ("failing", False)
    assert row["line"] == "hold: grok on build-a 1.0.71 (node): install failed; daily: could not start: OSError: no fork"


def test_a_day_without_issues_is_a_quiet_passing_check(tick):
    root, run = tick

    [row] = run(86400.0 * 400)

    assert (row["state"], row["ok"], row["line"]) == ("ok", True, "no issues")
    written = lina_runner_sync_daily.health_path(root).read_bytes()
    run(86400.0 * 400 + 30)
    assert lina_runner_sync_daily.health_path(root).read_bytes() == written, "an unchanged day is not rewritten"
