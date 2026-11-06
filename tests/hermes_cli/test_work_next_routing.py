"""``lina work next --submit`` routes at hand-off (lina#283): the issue's labels and size go to submit, the first
``worker.routing`` rule that holds picks the runner, else the runner label's runner as before. The fake hub decides
the way ``Hub.make_job`` does (``lina_work_settings_route.decide``) and records what it was given."""
from __future__ import annotations

import textwrap
from pathlib import Path

import pytest

from hermes_cli.delivery import next as picker
from hermes_cli.delivery import settings
from tests.hermes_cli._work_fakes import FakeGh, FakeRecord

REPO = "lina-system/lina"
RUNNERS = """\
worker:
  runners:
    dsh: {type: command, argv: [dsh]}
    codex: {type: command, argv: [codex]}
    idle: {type: command, argv: [idle], "on": false}
"""


class FakeHub:
    def __init__(self, base: Path):
        self.base, self.given, self.decided = base, [], []

    def submit(self, opts):
        from hermes_cli.lina_work_settings import SettingsError
        from hermes_cli.lina_work_settings_route import decide
        from hermes_cli.worker.hub import HubError
        self.given.append({k: opts[k] for k in ("runner", "labels", "size") if k in opts})
        try:
            made = decide(self.base, opts.get("runner"), opts["mode"], None, opts.get("labels"), opts.get("size"))
        except SettingsError as exc:
            raise HubError(str(exc)) from exc
        self.decided.append((made.runner, made.settings["runner_source"]))


def _gh(issues: list[dict]) -> FakeGh:
    rows = [{"number": n, "title": f"feat(x{n}): issue {n}", "body": "Sprint: M0 S3\n" + body,
             "labels": [{"name": label} for label in labels], "issue_dependencies_summary": {"total_blocked_by": 0}}
            for n, body, labels in issues]
    return FakeGh({f"repos/{REPO}/issues": rows, f"repos/{REPO}/pulls": [], f"repos/{REPO}/branches": [],
                   f"repos/{REPO}/actions/runs": {"workflow_runs": []}})


def _run(tmp_path: Path, routing: str, issues: list[tuple]) -> tuple[dict, FakeHub]:
    tmp_path.mkdir(parents=True, exist_ok=True)
    (tmp_path / "config.yaml").write_text(RUNNERS + textwrap.indent(routing, "  "), encoding="utf-8")
    settings.set_sprint("M0 S3", tmp_path)
    hub = FakeHub(tmp_path)
    plan, _ = picker.run_next(_gh(issues), [REPO], FakeRecord(), submit_picks=True, base=tmp_path, hub=hub)
    return plan, hub


@pytest.fixture(autouse=True)
def _home(tmp_path, monkeypatch):
    monkeypatch.setenv("LINA_HOME", str(tmp_path))
    monkeypatch.delenv("HERMES_KANBAN_TASK", raising=False)


def test_a_routing_rule_sends_a_labelled_issue_to_another_runner_with_the_size_from_its_label(tmp_path):
    rule = "routing:\n  - {name: mid, when: {runner_label: dsh, size: [M]}, runner: codex}\n"
    plan, hub = _run(tmp_path, rule, [(1, "", ["dsh", "size/M"])])

    assert hub.given == [{"labels": ["dsh", "size/M"], "size": "M"}]
    assert hub.decided == [("codex", "routing:mid")]
    assert picker.lines_of(plan)[0].startswith(f"open {REPO}#1 (codex,")


def test_without_routing_rules_an_issue_goes_to_its_label_s_runner(tmp_path):
    plan, hub = _run(tmp_path, "", [(1, "", ["dsh", "size/M"]), (2, "- Size: S\n", ["dsh"])])

    assert sorted(hub.given, key=lambda g: g["size"]) == [
        {"runner": "dsh", "labels": ["dsh", "size/M"], "size": "M"}, {"runner": "dsh", "labels": ["dsh"], "size": "S"}]
    assert hub.decided == [("dsh", "--runner")] * 2
    assert all(d["submit"]["ok"] for d in plan["decisions"] if d["action"] == "open")


def test_a_rule_to_an_off_runner_never_sends_there_and_an_off_runner_is_refused_with_one_line(tmp_path):
    rule = "routing:\n  - {name: parked, when: {runner_label: dsh}, runner: idle}\n"
    plan, hub = _run(tmp_path, rule, [(1, "", ["dsh", "size/S"]), (2, "", ["idle", "size/L"])])

    assert hub.given == [{"runner": "dsh", "labels": ["dsh", "size/S"], "size": "S"},
                         {"runner": "idle", "labels": ["idle", "size/L"], "size": "L"}]
    assert hub.decided == [("dsh", "--runner")]
    refused = next(d for d in plan["decisions"] if d["issue"] == 2)
    assert refused["submit"] == {"ok": False, "why": "worker.runners.idle.on: runner 'idle' is off"}
