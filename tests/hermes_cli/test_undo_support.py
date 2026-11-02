"""Synthetic undo fixtures using the existing LINA work sandbox."""

from __future__ import annotations

from contextlib import redirect_stderr, redirect_stdout
import io
import json

import pytest

from tests.hermes_cli._work_env import WorkEnv


class UndoEnv(WorkEnv):
    def __init__(self, tmp_path, monkeypatch):
        super().__init__(tmp_path, monkeypatch, locale="en")
        monkeypatch.setenv("LINA_HOME", str(self.root))
        self.profiles = {"default": self.root}
        for name in ("alpha", "beta"):
            home = self.root / "profiles" / name
            home.mkdir(parents=True)
            (home / "profile.yaml").write_text("name: " + name)
            self.profiles[name] = home
        for home in self.profiles.values():
            (home / "memories").mkdir()
            (home / "skills").mkdir()
        self.memory = self.profiles["alpha"] / "memories/MEMORY.md"
        self.memory.write_bytes(b"synthetic original\r\n\x00\xff")
        self.rule = self.desk / "rule.md"
        self.rule.write_bytes(b"synthetic rule\n")
        self.config("en", f"undo:\n  rules: ['{self.desk}/*rule.md']\n")

    def run(self, *args):
        from hermes_cli.undo import main

        out, err = io.StringIO(), io.StringIO()
        with redirect_stdout(out), redirect_stderr(err):
            try:
                code = main(list(args))
            except SystemExit as exc:
                code = exc.code
        return code, out.getvalue(), err.getvalue()

    def begin(self, *paths, bot="alpha", what="Fixture edit"):
        args = ["begin", "--bot", bot, "--what", what]
        for path in paths:
            args += ["--path", str(path)]
        return self.ok(*args).strip()

    @property
    def undo_root(self):
        return self.root / "undo"

    def pending(self, change_id):
        return self.undo_root / "pending" / (change_id + ".json")

    def journal(self):
        path = self.undo_root / "journal.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def log(self, refs, *, change_id="change-1", kind="revert", until="", what="Fixture change"):
        return self.run("log", "--bot", "alpha", "--change-id", change_id, "--what", what,
                        "--objects", json.dumps(refs), "--kind", kind, "--until", until,
                        "--how", "Owning bot restores referenced objects")


@pytest.fixture
def undo(tmp_path, monkeypatch):
    return UndoEnv(tmp_path, monkeypatch)
