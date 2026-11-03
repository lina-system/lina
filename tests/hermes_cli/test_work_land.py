"""Landing a ``lina worker`` branch (lina#63): fetched from the task's work folder on the device as it is, ``dev``
merged into it, never re-applied as patches. Real git repositories in a temp dir stand in for GitHub and the device:
a branch whose history carries a subtree merge keeps that merge and its files after landing, and a commit by an
author outside the project's noreply address stops the landing before anything is pushed."""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from hermes_cli import merge_rules as rules
from hermes_cli.delivery.land import LandError, land
from hermes_cli.worker import device
from hermes_cli.worker.hub_store import Store
from tests.hermes_cli._work_fakes import FakeGh

REPO, TASK, BRANCH = "acme/app", "OT-LAND1", "feat/12-import"
BOT = ("Lina Bot", "1+lina-bot@users.noreply.github.com")


def git(cwd: Path, *args: str, who: tuple[str, str] = BOT) -> str:
    done = subprocess.run(["git", "-c", f"user.name={who[0]}", "-c", f"user.email={who[1]}",
                           "-c", "init.defaultBranch=dev", *args], cwd=cwd, capture_output=True, text=True, check=True)
    return done.stdout.strip()


def commit(cwd: Path, name: str, text: str, message: str, who=BOT) -> None:
    (cwd / name).parent.mkdir(parents=True, exist_ok=True)
    (cwd / name).write_text(text, encoding="utf-8")
    git(cwd, "add", name, who=who)
    git(cwd, "commit", "-q", "-m", message, who=who)


@pytest.fixture
def world(tmp_path):
    origin = tmp_path / "origin" / f"{REPO}.git"
    origin.mkdir(parents=True)
    git(origin, "init", "-q", "--bare")
    seed = tmp_path / "seed"
    seed.mkdir()
    git(seed, "init", "-q")
    commit(seed, "README.md", "app\n", "docs: start")
    commit(seed, "CHANGELOG.md", "# Changelog\n\n## [Unreleased]\n", "docs: changelog")
    git(seed, "push", "-q", str(origin), "HEAD:refs/heads/dev")

    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(
        "devices:\n  workers:\n    here: {host: local, cap: 1}\n"
        f"worker:\n  repo_url: {tmp_path / 'origin'}/{{repo}}.git\n"
        f"work:\n  author: {BOT[0]} <{BOT[1]}>\n  forbidden_patterns: ['private-host-name']\n", encoding="utf-8")
    root = base / "worker"
    Store(root).save({"id": TASK, "repo": REPO, "host": "here", "bot": "sion", "created": 0, "branch": f"omo/{TASK}"})
    work = device.task_of(root, TASK).work
    work.parent.mkdir(parents=True)
    git(tmp_path, "clone", "-q", "-b", "dev", str(origin), str(work))
    git(work, "switch", "-q", "-c", f"omo/{TASK}")

    vendor = tmp_path / "vendor"
    vendor.mkdir()
    git(vendor, "init", "-q")
    commit(vendor, "server/main.py", "print('server')\n", "server: first")
    commit(vendor, "server/util.py", "X = 1\n", "server: util")

    commit(seed, "docs/later.md", "later\n", "docs: dev moves on")
    git(seed, "push", "-q", str(origin), "HEAD:refs/heads/dev")
    return {"tmp": tmp_path, "origin": origin, "base": base, "work": work, "vendor": vendor}


def import_subtree(work: Path, vendor: Path) -> None:
    git(work, "fetch", "-q", str(vendor), "dev:refs/remotes/vendor/dev")
    git(work, "merge", "-q", "-s", "ours", "--no-commit", "--allow-unrelated-histories", "vendor/dev")
    git(work, "read-tree", "--prefix=vendor/", "-u", "vendor/dev")
    git(work, "commit", "-q", "-m", "feat(vendor): import the server as a subtree")


def fake_github() -> FakeGh:
    head = {"sha": None}

    def pulls(query: str):
        return [{"number": 31}] if "state=open" in query and head["sha"] else []

    def pull(_query):
        return {"number": 31, "state": "open", "merged": False, "node_id": "PR_31", "body": "Closes #12",
                "head": {"sha": head["sha"], "ref": BRANCH}, "base": {"ref": "dev"}, "labels": []}

    def on_run(args):
        if args[:2] == ["pr", "create"]:
            body = Path(args[args.index("--body-file") + 1]).read_text(encoding="utf-8")
            head["body"] = body
            head["sha"] = "f" * 40
        return ""

    gh = FakeGh({f"repos/{REPO}/pulls": pulls, f"repos/{REPO}/pulls/31": pull,
                 f"repos/{REPO}/rules/branches/dev": [{"type": "required_status_checks", "parameters": {
                     "required_status_checks": [{"context": "tests"}]}}],
                 f"repos/{REPO}/actions/runs": {"workflow_runs": []},
                 f"repos/{REPO}/commits/{'f' * 40}/check-runs": {"total_count": 0, "check_runs": []},
                 f"repos/{REPO}/commits/{'f' * 40}/statuses": [], f"repos/{REPO}/pulls/31/reviews": [],
                 f"repos/{REPO}/pulls/31/comments": [], f"repos/{REPO}/issues/31/comments": [],
                 f"repos/{REPO}/issues/12": {"number": 12, "state": "open", "body": ""}}, on_run=on_run)
    gh.created = head
    return gh


def test_a_subtree_merge_survives_the_landing_and_dev_is_merged_in(world):
    work = world["work"]
    commit(work, "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    import_subtree(work, world["vendor"])
    worker_head = git(work, "rev-parse", "HEAD")
    gh, lines = fake_github(), []

    code = land(gh, BRANCH, TASK, "feat(vendor): import the server", 12, base=world["base"], out=lines.append)

    assert code == rules.WAIT, lines  # the new head has no checks yet: the gates wait, nothing merges
    origin = world["origin"]
    landed = git(origin, "rev-parse", f"refs/heads/{BRANCH}")
    for ancestor in (worker_head, git(origin, "rev-parse", "refs/heads/dev")):
        subprocess.run(["git", "merge-base", "--is-ancestor", ancestor, landed], cwd=origin, check=True)
    merges = git(origin, "log", "--merges", "--format=%s", landed)
    assert "feat(vendor): import the server as a subtree" in merges
    files = git(origin, "ls-tree", "-r", "--name-only", landed).splitlines()
    assert {"vendor/server/main.py", "vendor/server/util.py", "app/feature.py", "docs/later.md"} <= set(files)
    create = next(c for c in gh.commands if c[:2] == ["pr", "create"])
    assert create[create.index("--head") + 1] == BRANCH and create[create.index("--base") + 1] == "dev"
    assert "Closes #12" in gh.created["body"]
    assert not any(c[:2] == ["pr", "merge"] for c in gh.commands)


def test_a_commit_by_a_foreign_author_stops_the_landing_before_any_push(world):
    work = world["work"]
    commit(work, "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    commit(work, "app/more.py", "MORE = 1\n", "feat(app): more", who=("Someone", "someone@example.com"))
    gh = fake_github()

    with pytest.raises(LandError, match="not the project's noreply address"):
        land(gh, BRANCH, TASK, "feat(app): the feature", 12, base=world["base"], out=lambda _line: None)

    refs = git(world["origin"], "for-each-ref", "--format=%(refname)")
    assert f"refs/heads/{BRANCH}" not in refs
    assert gh.commands == []


def test_a_forbidden_name_in_an_added_line_stops_the_landing(world):
    commit(world["work"], "docs/hosts.md", "ssh private-host-name\n", "docs: hosts")
    with pytest.raises(LandError, match="forbidden_patterns"):
        land(fake_github(), BRANCH, TASK, "docs: hosts", 12, base=world["base"], out=lambda _line: None)
    assert f"refs/heads/{BRANCH}" not in git(world["origin"], "for-each-ref", "--format=%(refname)")


def test_a_remote_device_hands_out_the_branch_as_a_bundle_and_its_merge_survives(world, monkeypatch):
    from hermes_cli.worker import hub as hub_module
    from hermes_cli.worker.transport import LocalTransport
    base, root = world["base"], world["base"] / "worker"
    config = base / "config.yaml"
    config.write_text(config.read_text(encoding="utf-8").replace(
        "here: {host: local, cap: 1}", "remote-a: {host: remote-a, cap: 1}"), encoding="utf-8")
    Store(root).save({"id": TASK, "repo": REPO, "host": "remote-a", "bot": "sion", "created": 0})
    calls = []

    class Recording(LocalTransport):
        def call(self, verb, stdin=b"", timeout=120):
            calls.append(verb)
            return super().call(verb, stdin, timeout)

    monkeypatch.setattr(hub_module, "for_device", lambda dev, worker_root: Recording(worker_root))
    work = world["work"]
    commit(work, "app/feature.py", "VALUE = 2\n", "feat(app): the feature")
    import_subtree(work, world["vendor"])
    before = (git(work, "rev-parse", "HEAD"), git(work, "status", "--porcelain"))

    code = land(fake_github(), BRANCH, TASK, "feat(vendor): import the server", 12, base=base, out=lambda _line: None)

    assert code == rules.WAIT
    assert calls == [["bundle", TASK, "HEAD"]]
    assert (git(work, "rev-parse", "HEAD"), git(work, "status", "--porcelain")) == before, "bundle is read-only"
    landed = git(world["origin"], "rev-parse", f"refs/heads/{BRANCH}")
    subprocess.run(["git", "merge-base", "--is-ancestor", before[0], landed], cwd=world["origin"], check=True)
    assert "feat(vendor): import the server as a subtree" in git(world["origin"], "log", "--merges", "--format=%s",
                                                                  landed)
    assert "vendor/server/main.py" in git(world["origin"], "ls-tree", "-r", "--name-only", landed)


def test_the_bundle_verb_refuses_a_task_without_a_work_repository(tmp_path):
    (tmp_path / "lina").mkdir()
    code, out = device.dispatch(tmp_path / "lina" / "worker", ["bundle", "OT-NONE"])
    assert code == 1 and b"no git work folder" in out


def test_changelog_conflicts_are_kept_both_only_when_they_are_two_plain_sides():
    """A CHANGELOG conflict is resolved by keeping both sides only in the plain two-way form; a diff3 base
    section or CRLF lines once went through as conflict text in the pushed result."""
    from hermes_cli.delivery.land import _keep_both
    two = "a\n<<<<<<< HEAD\nours\n=======\ntheirs\n>>>>>>> dev\nb\n"
    assert _keep_both(two) == "a\nours\ntheirs\nb\n"
    assert _keep_both("a\n<<<<<<< HEAD\nours\n||||||| base\nold\n=======\ntheirs\n>>>>>>> dev\nb\n") is None
    assert _keep_both(two.replace("\n", "\r\n")) is None
    assert _keep_both("a\n<<<<<<< HEAD\nunclosed\n") is None
