"""work.land_merge (2026-11-23): `lina work merge --branch` opens the PR and stops; merges only when it is on."""
from __future__ import annotations

import pytest

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import merge as merge_module
from hermes_cli.delivery.land import land
from tests.hermes_cli.test_work_land import BRANCH, TASK, commit, fake_github

pytest_plugins = ["tests.hermes_cli.test_work_land"]


class _Pass:
    code = rules.PASS
    lines = [(rules.PASS, "every gate passes")]

    def text(self):
        return ["PASS every gate passes"]


def test_gates_passing_still_only_opens_the_pr_by_default(world, monkeypatch):
    monkeypatch.setattr(merge_module.rules, "may_merge", lambda snapshot: _Pass())
    commit(world["work"], "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    gh, lines = fake_github(), []
    code = land(gh, BRANCH, TASK, "feat(app): the feature", 12, base=world["base"], out=lines.append)
    assert code == rules.PASS, lines
    assert [c[:2] for c in gh.commands].count(["pr", "create"]) == 1
    assert not any(c[:2] == ["pr", "merge"] for c in gh.commands)
    assert not any("enqueuePullRequest" in q for q, _ in gh.queries)
    assert any("not merged: PR only" in line for line in lines)


def test_land_merge_true_keeps_the_old_merge(world, monkeypatch):
    monkeypatch.setattr(merge_module.rules, "may_merge", lambda snapshot: _Pass())
    cfg = world["base"] / "config.yaml"
    cfg.write_text(cfg.read_text(encoding="utf-8").replace("work:\n", "work:\n  land_merge: true\n"), encoding="utf-8")
    commit(world["work"], "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    gh, lines = fake_github(), []
    land(gh, BRANCH, TASK, "feat(app): the feature", 12, base=world["base"], out=lines.append)
    assert any(c[:2] == ["pr", "merge"] for c in gh.commands), lines
