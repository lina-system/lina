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
from hermes_cli import lina_magpie

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


@pytest.fixture(autouse=True)
def _every_runner_keeps_its_type(tmp_path):
    yield
    from tests.hermes_cli._work_settings_scan import untyped_runners
    assert untyped_runners(tmp_path) == []


@pytest.fixture(params=["fresh", "today"])
def root(request, tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    if request.param == "today":
        (base / "config.yaml").write_text(TODAY, encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
    monkeypatch.setattr(lina_magpie, "get", lambda proxy, path: {"data": [{"id": "hub/openai/m-open"}]})
    monkeypatch.setattr(lina_magpie, "quota_accounts", lambda proxy: [])
    from hermes_cli import lina_work_settings_models
    lina_work_settings_models.forget()
    return base


def _config(base) -> dict:
    return hermes_yaml.safe_load((base / "config.yaml").read_text(encoding="utf-8")) or {}


def test_set_show_history_diff_and_rollback(root, capsys):
    assert _lina("settings", "set", "devices.workers.here", "{host: local, cap: 1}") == 0
    assert _lina("settings", "set", "worker.runners.dsh", "{type: command, argv: [dsh, '-']}") == 0
    assert _lina("settings", "set", "worker.runners.dsh.model", "codex/x:high") == 0
    out = capsys.readouterr().out
    assert "worker.runners.dsh.model: - → codex/x:high" in out
    assert _config(root)["worker"]["runners"]["dsh"]["model"] == "codex/x:high"

    assert _lina("settings", "show", "--json") == 0
    shown = json.loads(capsys.readouterr().out)
    assert shown["settings"]["devices"]["workers"]["here"] == {"host": "local", "cap": 1}
    last = shown["revision"]["id"]

    assert _lina("settings", "history", "--limit", "2") == 0
    lines = capsys.readouterr().out.splitlines()
    assert lines[0].startswith(last) and "cli" in lines[0] and "worker.runners.dsh.model" in lines[0]

    assert _lina("settings", "diff") == 0
    assert capsys.readouterr().out.strip() == "worker.runners.dsh.model: - → codex/x:high"

    assert _lina("settings", "rollback") == 0
    assert "rollback of" in capsys.readouterr().out
    assert "model" not in _config(root)["worker"]["runners"]["dsh"]


def test_removing_a_runner_that_holds_env_exits_2_and_writes_nothing(root, capsys):
    path = root / "config.yaml"
    path.write_text("worker:\n  runners:\n    codex: {type: command, argv: [codex, '-'], env: {SOME_HOME: /srv/x}}\n",
                    encoding="utf-8")
    before = path.read_bytes()
    assert _lina("settings", "set", "worker.runners.codex", "null") == 2
    err = capsys.readouterr().err
    assert err.startswith("lina worker settings: worker.runners.codex: has fields the work settings don't manage (env)")
    assert path.read_bytes() == before
    assert _lina("settings", "set", "worker.runners.codex.on", "false") == 0
    assert _config(root)["worker"]["runners"]["codex"]["env"] == {"SOME_HOME": "/srv/x"}


@pytest.mark.parametrize("text", ["false\n", "[]\n", "7\n", "devices: [unclosed\n"])
def test_a_config_that_is_not_a_readable_mapping_exits_2_and_is_left_alone(root, capsys, text):
    path = root / "config.yaml"
    path.write_text(text, encoding="utf-8")
    assert _lina("settings", "set", "devices.workers.here", "{host: local}") == 2
    last = capsys.readouterr().err.strip().splitlines()[-1]  # the config guard's own warning, naming the cause, comes first
    assert last.startswith("lina worker settings: config.yaml: ")
    assert path.read_text(encoding="utf-8") == text
    assert list((root / "work-settings").glob("revisions/r*.yaml")) == []


def _editor_writing(tmp_path, monkeypatch, text: str) -> None:
    saved = tmp_path / "saved.yaml"
    saved.write_text(text, encoding="utf-8")
    editor = tmp_path / "editor"
    editor.write_text(f"#!{sys.executable}\nimport shutil, sys\nshutil.copyfile({str(saved)!r}, sys.argv[1])\n",
                      encoding="utf-8")
    editor.chmod(editor.stat().st_mode | stat.S_IXUSR)
    monkeypatch.setenv("EDITOR", str(editor))
    monkeypatch.delenv("VISUAL", raising=False)


def _state(root) -> tuple:
    path = root / "config.yaml"
    return (path.read_bytes() if path.exists() else None,
            sorted(p.name for p in (root / "work-settings").glob("revisions/r*.yaml")))


NOT_MAPPINGS = ["false\n", "0\n", '""\n', "[]\n"]


@pytest.mark.parametrize("saved", NOT_MAPPINGS, ids=["false", "zero", "empty-text", "empty-list"])
def test_edit_saving_a_root_that_is_not_a_mapping_exits_2_and_writes_nothing(root, tmp_path, monkeypatch, capsys,
                                                                            saved):
    _lina("settings", "show")  # any outside revision of today's config is taken first
    before = _state(root)
    _editor_writing(tmp_path, monkeypatch, saved)
    assert _lina("settings", "edit") == 2
    assert capsys.readouterr().err.strip().splitlines()[-1].startswith("lina worker settings: ")
    assert _state(root) == before


@pytest.mark.parametrize("saved", ["", "   \n", "# only a comment\n"], ids=["empty", "blank", "comment"])
def test_edit_saving_an_empty_file_changes_nothing(root, tmp_path, monkeypatch, saved):
    _lina("settings", "show")
    before = _state(root)
    _editor_writing(tmp_path, monkeypatch, saved)
    assert _lina("settings", "edit") == 0
    assert _state(root) == before


@pytest.mark.parametrize("saved", NOT_MAPPINGS, ids=["false", "zero", "empty-text", "empty-list"])
def test_validate_a_file_whose_root_is_not_a_mapping_exits_1(root, tmp_path, capsys, saved):
    path = tmp_path / "candidate.yaml"
    path.write_text(saved, encoding="utf-8")
    assert _lina("settings", "validate", str(path)) == 1
    assert "the settings are a mapping" in capsys.readouterr().out


def test_settings_with_no_subcommand_shows_the_settings(root, capsys):
    assert _lina("settings") == 0
    assert capsys.readouterr().out.startswith("# r")


def test_history_catches_an_outside_edit_first(root, capsys):
    assert _lina("settings", "set", "devices.workers.here", "{host: local, cap: 1}") == 0
    path = root / "config.yaml"
    path.write_text(path.read_text(encoding="utf-8").replace("cap: 1", "cap: 4"), encoding="utf-8")
    capsys.readouterr()
    assert _lina("settings", "history", "--limit", "1") == 0
    line = capsys.readouterr().out.splitlines()[0]
    assert " outside " in line and "devices.workers.here.cap" in line


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
    assert table[0].split()[:4] == ["PROVIDER", "MODEL", "STATE", "LEFT"]
    assert table[1].split()[:4] == ["openai", "m-open", "no_account", "-"]
