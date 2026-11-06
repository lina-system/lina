"""``lina worker settings`` (lina#256) through the real ``lina worker`` parser, on a fresh LINA root and on today's
config: show, set, edit, validate, history, diff, rollback, route and models; a refusal exits 2 with one line."""

from __future__ import annotations

import argparse
import json
import stat
import sys
import textwrap

import pytest

import hermes_yaml
from hermes_cli import lina_models, lina_ocx

pytestmark = pytest.mark.platforms("posix")

TODAY = textwrap.dedent("""\
    devices:
      workers:
        build-a: {host: build-a, cap: 2}
    worker:
      runners:
        codex: {type: command, argv: [codex, "-"]}
      routing:
        - {name: docs, when: {labels_any: [docs]}, runner: codex}
    """)


def _lina(*argv: str) -> int:
    from hermes_cli.subcommands.worker import build_worker_parser
    parser = argparse.ArgumentParser(prog="lina")
    build_worker_parser(parser.add_subparsers(dest="command"))
    args = parser.parse_args(["worker", *argv])
    with pytest.raises(SystemExit) as done:
        args.func(args)
    return done.value.code or 0


@pytest.fixture(params=["fresh", "today"])
def root(request, tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    if request.param == "today":
        (base / "config.yaml").write_text(TODAY, encoding="utf-8")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: base)
    monkeypatch.setattr(lina_models, "run", lambda args, root=None: {
        ("models", "live", "--json"): [{"provider": "openai", "id": "m-open", "disabled": False,
                                        "reasoningEfforts": ["high"], "defaultReasoningEffort": "high"}],
        ("account", "list", "--quota", "--json"): {"accounts": []}}[tuple(args)])
    from hermes_cli import lina_work_settings_models
    lina_work_settings_models.forget()
    return base


def _config(base) -> dict:
    return hermes_yaml.safe_load((base / "config.yaml").read_text(encoding="utf-8")) or {}


def test_set_show_history_diff_and_rollback(root, capsys):
    assert _lina("settings", "set", "devices.workers.here", "{host: local, cap: 1}") == 0
    assert _lina("settings", "set", "worker.runners.dsh", "{type: command, argv: [dsh, '-']}") == 0
    assert _lina("settings", "set", "worker.runners.dsh.model", "opencodex/x:high") == 0
    out = capsys.readouterr().out
    assert "worker.runners.dsh.model: - → opencodex/x:high" in out
    assert _config(root)["worker"]["runners"]["dsh"]["model"] == "opencodex/x:high"

    assert _lina("settings", "show", "--json") == 0
    shown = json.loads(capsys.readouterr().out)
    assert shown["settings"]["devices"]["workers"]["here"] == {"host": "local", "cap": 1}
    last = shown["revision"]["id"]

    assert _lina("settings", "history", "--limit", "2") == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[0].startswith(last) and "cli" in lines[0] and "worker.runners.dsh.model" in lines[0]

    assert _lina("settings", "diff") == 0
    assert capsys.readouterr().out.strip() == "worker.runners.dsh.model: - → opencodex/x:high"

    assert _lina("settings", "rollback") == 0
    assert "rollback of" in capsys.readouterr().out
    assert "model" not in _config(root)["worker"]["runners"]["dsh"]


def test_a_refused_set_exits_2_with_one_line_and_writes_nothing(root, capsys):
    before = (root / "config.yaml").read_bytes() if (root / "config.yaml").exists() else None
    assert _lina("settings", "set", "devices.workers.laptop", "{role: person, cap: 1}") == 2
    err = capsys.readouterr().err
    assert err.startswith("lina worker settings: ") and "devices.workers.laptop.cap" in err
    after = (root / "config.yaml").read_bytes() if (root / "config.yaml").exists() else None
    assert after == before


def test_validate_a_file_and_the_current_settings(root, tmp_path, capsys):
    good = tmp_path / "good.yaml"
    good.write_text("devices:\n  workers:\n    here: {host: local}\n", encoding="utf-8")
    bad = tmp_path / "bad.yaml"
    bad.write_text("worker:\n  default_runner: ghost\n", encoding="utf-8")
    assert _lina("settings", "validate") == 0
    assert _lina("settings", "validate", str(good)) == 0
    assert capsys.readouterr().out.strip().endswith("ok")
    assert _lina("settings", "validate", str(bad)) == 1
    assert "worker.default_runner:" in capsys.readouterr().out


def test_edit_saves_what_the_editor_left(root, tmp_path, monkeypatch, capsys):
    editor = tmp_path / "editor"
    editor.write_text(f"#!{sys.executable}\nimport sys\nfrom pathlib import Path\n"
                      "p = Path(sys.argv[1])\np.write_text('devices:\\n  workers:\\n    here: {host: local, cap: 3}\\n')\n",
                      encoding="utf-8")
    editor.chmod(editor.stat().st_mode | stat.S_IXUSR)
    monkeypatch.setenv("EDITOR", str(editor))
    assert _lina("settings", "edit") == 0
    assert _config(root)["devices"]["workers"] == {"here": {"host": "local", "cap": 3}}
    assert "devices.workers.here" in capsys.readouterr().out


def test_route_and_models(root, capsys):
    _lina("settings", "set", "worker.runners.codex", "{type: command, argv: [codex, '-']}")
    _lina("settings", "set", "worker.routing", "[{name: docs, when: {labels_any: [docs]}, runner: codex}]")
    capsys.readouterr()
    assert _lina("settings", "route", "--labels", "docs,x", "--size", "S") == 0
    assert capsys.readouterr().out.strip() == "codex (rule docs)"
    assert _lina("settings", "route") == 0
    assert capsys.readouterr().out.strip() == "omo (default)"
    assert _lina("settings", "models") == 0
    table = capsys.readouterr().out.splitlines()
    assert table[0].split()[:4] == ["PROVIDER", "MODEL", "EFFORTS", "STATE"]
    assert table[1].split()[:4] == ["openai", "m-open", "high", "no_account"]
