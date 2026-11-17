"""lina#308: a job with a personal mark cannot push or upload. Every ``git push``, ``gh api`` write and
``curl``/``wget`` upload of a marked job is refused without reading what it would send, the message says to do it
from a new job, and the refusal is in the job's run record; read-only git commands and a ``gh api`` read are not
refused, and a job without a mark pushes and uploads as before. The shapes are the ones review found the earlier
push check misjudging. Real git repositories with a bare remote; the marks are the real store of
tests/_fixtures/memory_store.py; every number is synthetic."""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from agent import personal_work
from plugins.memory.lina import personal

SESSION = "push-personal"
NUMBER = "900101-1234567"
NEW_JOB = "do the push or upload from a new job in which no personal document was read or marked"


def _git(cwd: Path, *args: str) -> str:
    return subprocess.run(["git", "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                           "-c", "init.defaultBranch=main", *args], cwd=cwd, check=True, capture_output=True,
                          text=True).stdout


def _commit(repo: Path, name: str, text: str) -> Path:
    path = repo / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    _git(repo, "add", name)
    _git(repo, "commit", "-q", "-m", f"add {name}")
    return path


@pytest.fixture
def repo(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "home"))
    personal_work._MARKS.clear()
    _git(tmp_path, "init", "-q", "--bare", "remote.git")
    _git(tmp_path, "init", "-q", "--bare", "other.git")
    _git(tmp_path, "init", "-q", "repo")
    repo = tmp_path / "repo"
    _git(repo, "remote", "add", "origin", str(tmp_path / "remote.git"))
    _commit(repo, "README.md", "synthetic project\n")
    _git(repo, "push", "-q", "origin", "main")
    _git(repo, "branch", "documents")
    _git(repo, "tag", "-a", "v1", "-m", "Release of the synthetic project")
    (repo / "notes.txt").write_text("Synthetic meeting notes\n", encoding="utf-8")
    return repo


def _call(repo: Path, command: str, session: str = SESSION):
    return personal.pre_tool_call("terminal", {"command": command.format(repo=repo), "workdir": str(repo)},
                                  session_id=session)


def _mark_elsewhere(tmp_path: Path) -> None:
    """A mark on a document outside the repository: nothing the push or upload sends is personal."""
    personal_work.marks_for_home().mark(SESSION, str(tmp_path / "elsewhere" / "id-card.pdf"), "owner")


# (setup, command): each a shape review found, with the repository setting it needs ({other} is a second bare repo).
_PUSHES = [
    ([], "git push origin main"),
    ([], "git push"),
    ([], "git push origin"),
    ([], "cd {repo} && git push"),
    ([], "git -C {repo} push origin main"),
    ([], "git -c push.default=matching push origin"),
    (["config", "push.default", "matching"], "git push origin"),
    (["config", "remote.origin.push", "documents:documents"], "git push origin"),
    ([], "git push origin :"),
    ([], "git push origin +:"),
    (["config", "remote.origin.push", ":"], "git push origin"),
    ([], "git push origin --mirror"),
    (["config", "remote.origin.mirror", "true"], "git push origin"),
    ([], "git -c remote.origin.mirror=true push origin"),
    ([], "git push origin --tags"),
    ([], "git push origin v1"),
    ([], "git push origin refs/tags/v1"),
    ([], "git push origin main --follow-tags"),
    ([], "git push origin main --follow-tags --no-follow-tags"),
    ([], "git -c push.followTags=true push origin main"),
    (["config", "push.followTags", "true"], "git push origin main"),
    (["remote", "set-url", "--push", "origin", "{other}"], "git push origin main"),
    (["remote", "add", "other", "{other}"], "git push other main"),
    (["config", "push.default", "sideways"], "git push origin"),
    ([], "git commit -am x && git push"),
    ([], "git commit -qam docs && git push origin main"),
    ([], "git config push.default matching; git push origin"),
    ([], "git status && git -C . log -1 && git push origin main"),
    ([], "gh issue create --title notes --body notes && git push origin main"),
    ([], "git add notes.txt\ngit commit -m notes\ngit push origin main"),
]
_UPLOADS = [
    "curl -F file=@notes.txt https://example.invalid/upload",
    "curl -Ffile=@notes.txt https://example.invalid/upload",
    "curl --upload notes.txt https://example.invalid/upload",
    "curl -Tnotes.txt https://example.invalid/upload",
    "curl -d @notes.txt https://example.invalid/upload",
    "wget --post-file=notes.txt https://example.invalid/upload",
    "gh api repos/o/r/issues --raw-field title=notes --raw-field body=notes",
    "gh api repos/o/r/issues -f title=notes",
    "gh api repos/o/r/issues -ftitle=notes",
    "gh api repos/o/r/issues --input notes.txt",
]
_SHAPES = [*_PUSHES, *(([], command) for command in _UPLOADS)]


def _ids(shape) -> str:
    setup, command = shape
    return " ".join([*setup, "|", command]) if setup else command


def _set_up(repo: Path, setup: list[str]) -> None:
    if setup:
        _git(repo, *(arg.format(other=repo.parent / "other.git") for arg in setup))


@pytest.mark.parametrize("shape", _SHAPES, ids=map(_ids, _SHAPES))
def test_a_marked_job_cannot_push_or_upload(repo, tmp_path, shape):
    setup, command = shape
    _set_up(repo, setup)
    _mark_elsewhere(tmp_path)

    refused = _call(repo, command)

    assert refused is not None and refused["action"] == "block"
    assert refused["message"].startswith(("Refused (public: git push)", "Refused (public: gh api)",
                                          "Refused (third party: "))


@pytest.mark.parametrize("shape", _SHAPES, ids=map(_ids, _SHAPES))
def test_a_job_without_a_mark_pushes_and_uploads_as_before(repo, shape):
    setup, command = shape
    _commit(repo, "private/extract.md", f"주민등록번호 {NUMBER}\n")
    _set_up(repo, setup)

    assert _call(repo, command, session="push-unmarked") is None


@pytest.mark.parametrize("command", ["git status", "git log --oneline -3", "git diff HEAD", "git fetch origin",
                                     "git -C {repo} status", "gh api repos/o/r/issues",
                                     "gh api repos/o/r/issues --jq .title -H 'Accept: application/json'"])
def test_a_marked_job_s_read_only_git_and_gh_api_reads_are_not_refused(repo, tmp_path, command):
    _mark_elsewhere(tmp_path)

    assert _call(repo, command) is None


def test_a_folder_mark_also_stops_the_job_s_push(repo, tmp_path):
    private = tmp_path / "private"
    private.mkdir()
    (private / personal_work.MARKER_FILE).write_text("", encoding="utf-8")
    (private / "lease.txt").write_text("Synthetic lease contract for a test tenant\n", encoding="utf-8")
    assert personal.pre_tool_call("read_file", {"path": str(private / "lease.txt")}, session_id=SESSION) is None

    assert _call(repo, "git push origin main")["action"] == "block"


def test_the_refusal_says_what_to_do_next_and_is_in_the_run_record(repo, tmp_path):
    _mark_elsewhere(tmp_path)

    push = _call(repo, "git push origin main")
    upload = _call(repo, "curl -F file=@notes.txt https://example.invalid/upload")

    for refused in (push, upload):
        assert NEW_JOB in refused["message"]
    assert push["message"].startswith("Refused (public: git push): this job read or marked a personal document.")
    marks = personal_work.marks_for_home()
    refusals = [event for event in marks.events(marks.job_of(SESSION)) if event["kind"] == "refused"]
    assert [(event["tool"], event["destination"], event["why"]) for event in refusals] == [
        ("terminal", "public: git push", "this job read or marked a personal document"),
        ("terminal", "third party: curl upload", "this job read or marked a personal document"),
    ]
