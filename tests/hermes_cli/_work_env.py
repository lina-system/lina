"""A scratch LINA root for ``lina work`` tests: config with synthetic agents, a fixed clock, commands run in-process."""

from __future__ import annotations

import io
import json
import time
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path

NOW = 1800000000
FIXTURES = Path(__file__).resolve().parent / "fixtures" / "work"


class WorkEnv:
    def __init__(self, tmp_path: Path, monkeypatch, *, locale: str = "ko", extra: str = ""):
        self.tmp = tmp_path
        self.home = tmp_path / "home"
        self.root = self.home / ".lina"
        self.root.mkdir(parents=True)
        self.desk = self.home / "Desk"
        self.desk.mkdir()
        self.queue = self.home / "queue"
        monkeypatch.setenv("HOME", str(self.home))
        monkeypatch.setenv("HERMES_HOME", str(self.root))
        monkeypatch.setattr(Path, "home", lambda: self.home)
        self.clock = [float(NOW)]
        monkeypatch.setattr(time, "time", lambda: self.clock[0])
        self.config(locale, extra)

    def config(self, locale: str = "ko", extra: str = "") -> None:
        (self.root / "config.yaml").write_text(
            f"desk: {{path: {self.desk}}}\n"
            "work:\n"
            f"  locale: {locale}\n"
            "  owner: {id: owner, name: 주인}\n"
            "  agents: {alpha: 알파, beta: 베타}\n"
            f"  session_queue: {self.queue}\n"
            + ("" if "store:" in extra else "  store: {backend: sqlite}\n") + extra, encoding="utf-8")
        from hermes_cli.work import env
        env.reset()

    @property
    def work(self) -> Path:
        return self.root / "work"

    def run(self, *args: str) -> tuple[int, str, str]:
        from hermes_cli.work import env, main
        out, err = io.StringIO(), io.StringIO()
        code = 0
        env.reset()
        with redirect_stdout(out), redirect_stderr(err):
            try:
                main(list(args))
            except SystemExit as exc:
                code = exc.code if isinstance(exc.code, int) else 1
                if not isinstance(exc.code, int) and exc.code:
                    err.write(str(exc.code))
        return code, out.getvalue(), err.getvalue()

    def ok(self, *args: str) -> str:
        code, out, err = self.run(*args)
        assert code == 0, (args, code, out, err)
        return out

    def json(self, *args: str) -> dict:
        return json.loads(self.ok("json", *args))

    def notices(self, agent: str) -> str:
        path = self.work / "notices" / f"{agent}.log"
        return path.read_text(encoding="utf-8") if path.exists() else ""

    def fixture(self) -> Path:
        """The synthetic record of the golden files (an area, two goals, an edited to-do, a memo, a meta line), its
        checker state, a Desk project folder and a running session, imported into the store."""
        src = self.tmp / "ledger.jsonl"
        src.write_text((FIXTURES / "ledger.jsonl").read_text(encoding="utf-8"), encoding="utf-8")
        self.work.mkdir(parents=True, exist_ok=True)
        (self.work / "state.json").write_text((FIXTURES / "state.json").read_text(encoding="utf-8"), encoding="utf-8")
        (self.desk / "1_Projects/Finished").mkdir(parents=True)
        (self.queue / "running").mkdir(parents=True)
        (self.queue / "running/fixture.json").write_text(json.dumps({
            "id": "fixture", "profile": "alpha", "name": "fixture-topic", "state": "running",
            "created": NOW - 600, "started": NOW - 300}))
        self.ok("import", "--from", str(src))
        return src

    def goal_and_todos(self, *, check: str = "false", todos: int = 1) -> list[str]:
        """An area, one goal G1 and *todos* to-dos T1.. under it, each with its own check."""
        self.ok("add", "목표", "--by", "alpha", "--area", "Dev", "--title", "빌드 기기 정리", "--basis", "기기 정리해 줘",
                "--done", "빌드 기기가 모두 가동된다", "--check", "false")
        ids = []
        for n in range(todos):
            out = self.ok("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", f"기기 {n + 1} 점검",
                          "--why", "작업이 멈추지 않게", "--done", "기기가 답한다", "--check", check, "--omo", "next")
            ids.append(out.split()[0])
        return ids
