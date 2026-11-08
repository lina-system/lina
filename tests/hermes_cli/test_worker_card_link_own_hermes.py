"""A ``worker.card_link`` command that resolves to LINA's own ``hermes`` is refused, and a card link that keeps
failing is said once (lina#345).

Inside LINA a bare ``hermes`` on ``PATH`` is the running tree's own alias, not the Hermes Agent CLI, so every card
write would fail with ``kanban: board '<slug>' does not exist``. The running tree is faked in a temporary folder
(``sys.prefix`` of a virtual environment); every ``hermes`` here is a fake that records its argv."""

from __future__ import annotations

import json
import os
import sys
import textwrap
from pathlib import Path

import pytest

from hermes_cli.worker import kanban_link
from hermes_cli.worker.hub import CARD_FAILING_AFTER, Hub, HubError
from tests.hermes_cli import _worker_fakes as fakes

CARD, BOARD = "t_card1", "ops"

FAKE_HERMES = """import json, os, sys
with open(os.environ["FAKE_HERMES_LOG"], "a") as fh:
    fh.write(json.dumps(sys.argv[1:]) + "\\n")
if os.environ.get("FAKE_HERMES_FAIL"):
    print("kanban: board 'ops' does not exist", file=sys.stderr)
    sys.exit(1)
if "show" in sys.argv:
    print(json.dumps({"task": {"status": "scheduled"}}))
"""


def _executable(path: Path, body: str) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body, encoding="utf-8")
    path.chmod(0o755)
    return path


@pytest.fixture
def lina_tree(tmp_path, monkeypatch):
    """A running LINA whose virtual environment is *tree*, with its own ``hermes`` first on ``PATH``."""
    tree = (tmp_path / "lina-tree").resolve()
    own = _executable(tree / ".hermes" / "bin" / "hermes", f"#!{sys.executable}\n" + FAKE_HERMES)
    monkeypatch.setattr(sys, "prefix", str(tree))
    monkeypatch.setenv("PATH", f"{own.parent}{os.pathsep}{os.environ['PATH']}")
    monkeypatch.setenv("FAKE_HERMES_LOG", str(tmp_path / "hermes.jsonl"))
    return own


def _calls(tmp_path: Path) -> list[list[str]]:
    log = tmp_path / "hermes.jsonl"
    return [json.loads(line) for line in log.read_text(encoding="utf-8").splitlines()] if log.is_file() else []


def _config(base: Path, command: list[str]) -> None:
    base.mkdir(parents=True, exist_ok=True)
    (base / "config.yaml").write_text(
        "devices:\n  workers: {}\nworker:\n  poll_s: 0.05\n  min_free_gb: 0\n"
        f"  card_link: {json.dumps({'kind': 'hermes', 'command': command})}\n  runners: {{}}\n", encoding="utf-8")


def _submit(tmp_path: Path, base: Path) -> str:
    lines: list[str] = []
    Hub(base / "worker", out=lines.append).submit({
        "briefs": [fakes.brief(tmp_path, "do it")], "mode": "ulw", "bot": "agent-a", "topic": "t", "wait": None,
        "card": {"task": CARD, "board": BOARD}})
    return lines[0].split()[0]


def _status(base: Path, task_id: str) -> list[str]:
    lines: list[str] = []
    Hub(base / "worker", out=lines.append).status(task_id)
    return lines


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("via", ["path", "user-bin forwarder"])
def test_a_bare_hermes_that_resolves_into_the_running_lina_is_refused_before_any_card_write(tmp_path, monkeypatch,
                                                                                            lina_tree, via):
    found = lina_tree
    if via == "user-bin forwarder":  # ~/.local/bin/hermes: `exec <tree>/.hermes/bin/hermes "$@"`
        found = _executable(tmp_path / "user-bin" / "hermes", f'#!/bin/sh\nexec {lina_tree} "$@"\n')
        monkeypatch.setenv("PATH", f"{found.parent}{os.pathsep}{os.environ['PATH']}")
    base = tmp_path / "lina"
    _config(base, ["hermes"])

    with pytest.raises(kanban_link.CardLinkError) as refused:
        kanban_link.config({"card_link": {"kind": "hermes", "command": ["hermes"]}})
    assert str(refused.value) == (f"worker.card_link.command[0] 'hermes' is this LINA's own {found}; "
                                  "set the Hermes Agent CLI by absolute path")
    with pytest.raises(HubError, match="is this LINA's own"):
        _submit(tmp_path, base)
    assert _calls(tmp_path) == []


@pytest.mark.platforms("posix")
def test_a_hermes_by_absolute_path_outside_the_lina_tree_is_used_and_the_start_comment_goes(tmp_path, lina_tree):
    agent = _executable(tmp_path / "hermes-agent" / "bin" / "hermes", f"#!{sys.executable}\n" + FAKE_HERMES)
    base = tmp_path / "lina"
    _config(base, [str(agent)])

    task_id = _submit(tmp_path, base)
    Hub(base / "worker", out=lambda *_: None).tick()

    comments = [argv for argv in _calls(tmp_path) if "comment" in argv]
    assert len(comments) == 1 and f"run {task_id} handed out" in comments[0][-1]
    assert not any("card link failing" in line for line in _status(base, task_id))


@pytest.mark.platforms("posix")
def test_a_card_start_that_keeps_failing_is_said_once_on_the_hub_status_and_end_notice(tmp_path, monkeypatch,
                                                                                       lina_tree):
    agent = _executable(tmp_path / "hermes-agent" / "bin" / "hermes", f"#!{sys.executable}\n" + FAKE_HERMES)
    monkeypatch.setenv("FAKE_HERMES_FAIL", "1")
    base = tmp_path / "lina"
    _config(base, [str(agent)])
    task_id = _submit(tmp_path, base)
    out: list[str] = []
    hub = Hub(base / "worker", out=out.append)

    for _ in range(CARD_FAILING_AFTER + 2):
        hub.tick()
    hub.notify(task_id, "done", "1")

    tries = CARD_FAILING_AFTER + 3  # every tick, then the end's own attempt
    # The hub keeps the first 200 characters of the first error line, and the fake CLI's tmp path is part of it, so
    # the kept text depends on how long the platform's tmp path is (macOS's is long): build it the same way.
    error = (f"{agent} kanban --board {BOARD} comment {CARD} exited 1: kanban: board 'ops' does not exist")[:200]
    line = f"card link failing: {error} ({tries} tries)"
    assert len(_calls(tmp_path)) == tries
    said = [text for text in out if "card link failing" in text]
    assert said == [f"lina worker: {task_id} card link failing: {error} ({CARD_FAILING_AFTER} tries)"]
    assert [text for text in _status(base, task_id) if "card link failing" in text] == [f"  {line}"]
    ends = [entry["text"] for entry in hub.store.load(task_id)["card_pending"].values()]
    assert len(ends) == 1 and ends[0].count("card link failing") == 1


@pytest.mark.platforms("posix")
def test_the_end_steps_refuse_lina_own_hermes_with_the_same_verdict_and_say_it_once(tmp_path, lina_tree):
    agent = _executable(tmp_path / "hermes-agent" / "bin" / "hermes", f"#!{sys.executable}\n" + FAKE_HERMES)
    base = tmp_path / "lina"
    _config(base, [str(agent)])
    task_id = _submit(tmp_path, base)
    Hub(base / "worker", out=lambda *_: None).tick()  # the start comment goes through the Hermes Agent CLI
    _config(base, ["hermes"])  # then the hub's config names LINA's own
    out: list[str] = []
    hub = Hub(base / "worker", out=out.append)

    hub.notify(task_id, "done", "1")
    for _ in range(CARD_FAILING_AFTER):
        hub.tick()

    assert [argv[argv.index(BOARD) + 1] for argv in _calls(tmp_path)] == ["comment"]
    status = [text for text in _status(base, task_id) if "card link failing" in text]
    assert len(status) == 1 and f"is this LINA's own {lina_tree}" in status[0]
    assert len([text for text in out if "card link failing" in text]) == 1


def test_a_card_that_has_not_scheduled_itself_yet_is_waiting_not_failing(tmp_path):
    """``wake`` retries a card still ``running``: that is the card's own pace, never a failing link."""
    show = _executable(tmp_path / "show", f"#!{sys.executable}\n" + textwrap.dedent("""\
        import json
        print(json.dumps({"task": {"status": "running"}}))
        """))

    class Store:
        root = tmp_path / "hub"

        def lock(self, name):
            import contextlib
            return contextlib.nullcontext()
    with pytest.raises(kanban_link.CardWaiting):
        kanban_link.wake(Store(), {"task": CARD, "board": BOARD}, {"command": [str(show)]}, "OT-X0000000001")
