"""Live progress and concurrent callers of the branch landing command."""
import os
from concurrent.futures import ThreadPoolExecutor
from contextlib import redirect_stdout
from threading import Event

import pytest

from hermes_cli.delivery import land as landing
from hermes_cli.worker.hub_store import Store
from tests.hermes_cli._work_fakes import FakeGh


@pytest.fixture
def run_land(tmp_path, monkeypatch):
    repo, task = "acme/app", "OT-LAND"
    Store(tmp_path / "worker").save({"id": task, "repo": repo, "bot": "worker"})
    open_pulls = {}

    def git(command, *args, **kwargs):
        if command == "rev-list":
            return "1" if args[-1].startswith("origin/dev..") else "0"
        if command in ("rev-parse", "merge-base"):
            return "pushed-head"
        return ""

    monkeypatch.setattr(landing, "git", git)
    monkeypatch.setattr(landing, "merge_base_into", lambda *args: None)
    monkeypatch.setattr(landing, "check_result", lambda *args: None)
    monkeypatch.setattr(landing.WorkerBranchSource, "fetch", lambda *args: "worker-head")
    gh = FakeGh({f"repos/{repo}/pulls": lambda query: open_pulls.get(query.rsplit(":", 1)[1], []),
                 f"repos/{repo}/issues/1": {}})

    def create(args):
        branch = args[args.index("--head") + 1]
        open_pulls[branch] = [{"number": 31, "head": {"ref": branch}, "base": {"ref": "dev"}, "state": "open"}]
        return ""

    gh.on_run = create
    monkeypatch.setattr(landing, "report_only", lambda *args, **kwargs: 75)

    def run(branch, **options):
        return landing.land(gh, branch, task, "feat: feature", 1, base=tmp_path, **options)

    return run, gh


def test_pipe_receives_progress_before_each_slow_operation(run_land, monkeypatch):
    run, gh = run_land
    reader, writer = os.pipe()
    with os.fdopen(reader, "rb", buffering=0) as pipe, os.fdopen(writer, "w") as stdout:
        os.set_blocking(reader, False)
        assert not stdout.isatty() and not stdout.line_buffering
        seen = []

        def observe(step):
            text = (pipe.read() or b"").decode()
            assert step in text.lower(), (step, text)
            seen.append(step)

        def observe_call(owner, name, step):
            original = getattr(owner, name)

            def observed(*args, **kwargs):
                observe(step)
                return original(*args, **kwargs)

            monkeypatch.setattr(owner, name, observed)

        git = landing.git

        def observe_git(command, *args, **kwargs):
            if command in {"clone", "push"}:
                observe("clon" if command == "clone" else "push")
            return git(command, *args, **kwargs)

        monkeypatch.setattr(landing, "git", observe_git)
        observe_call(landing.WorkerBranchSource, "fetch", "fetch")
        observe_call(landing, "merge_base_into", "merg")
        observe_call(landing, "check_result", "landing checks")
        observe_call(gh, "on_run", "pull request")
        with redirect_stdout(stdout):
            run("feat/example")
        assert seen == ["clon", "fetch", "merg", "landing checks", "push", "pull request"]


def test_same_branch_reuses_pr_while_an_independent_branch_completes(run_land):
    run, gh = run_land
    branch, other = "feat/example", "feat-example"
    creating, contender, release = Event(), Event(), Event()
    waiting_lines = []
    publish = gh.on_run

    def hold_creation(args):
        if args[args.index("--head") + 1] == branch:
            if creating.is_set():
                contender.set()  # On the unlocked implementation both attempts reach GitHub.
            creating.set()
            assert release.wait(30)
        return publish(args)

    def record(line):
        waiting_lines.append(line)
        if "waiting" in line.lower():
            contender.set()

    gh.on_run = hold_creation
    with ThreadPoolExecutor(max_workers=3) as pool:
        first = pool.submit(run, branch)
        try:
            assert creating.wait(10)
            second = pool.submit(run, branch, out=record)
            assert contender.wait(10)
            assert pool.submit(run, other).result(timeout=10) == 75
        finally:
            release.set()
        assert first.result(timeout=10) == 75
        result = second.result(timeout=10)
    created = [args[args.index("--head") + 1] for args in gh.commands if args[:2] == ["pr", "create"]]
    assert created.count(branch) == created.count(other) == 1, created
    assert result == 0
    assert any("#31" in line and "already open" in line for line in waiting_lines)
