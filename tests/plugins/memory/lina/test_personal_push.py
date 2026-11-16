"""lina#308: a ``git push`` from a job with a personal document is checked on what it would send, not only on its
words. The commits not yet on the remote's tracking refs are refused when they carry a marked document or an ID or
account number, allowed when they carry neither, and refused when they cannot be told (no tracking ref). A push from a
job without a mark is not checked. Real git repositories with a bare remote; the marks are the real store of
tests/_fixtures/memory_store.py; every number is synthetic."""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from agent import personal_work
from plugins.memory.lina import personal

SESSION = "push-personal"
NUMBER = "900101-1234567"


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
    _git(tmp_path, "init", "-q", "repo")
    repo = tmp_path / "repo"
    _git(repo, "remote", "add", "origin", str(tmp_path / "remote.git"))
    _commit(repo, "README.md", "synthetic project\n")
    _git(repo, "push", "-q", "origin", "main")
    return repo


def _push(repo: Path, command: str = "git push origin main", session: str = SESSION):
    return personal.pre_tool_call("terminal", {"command": command, "workdir": str(repo)}, session_id=session)


def _mark(path: Path) -> None:
    personal_work.marks_for_home().mark(SESSION, str(path.resolve()), "owner")


def test_a_push_of_a_committed_personal_document_is_refused(repo):
    document = _commit(repo, "private/contract.txt", "Synthetic lease contract for a test tenant\n")
    _mark(document)

    refused = _push(repo)

    assert refused["action"] == "block"
    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry the personal "
                                         f"document {document.resolve()}")


def test_a_push_whose_commits_add_an_id_number_is_refused(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _commit(repo, "notes/extract.md", f"주민등록번호 {NUMBER}\n")

    refused = _push(repo, f"cd {repo} && git push")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry an ID or account "
                                         "number")


def test_a_push_of_clean_commits_from_a_marked_job_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _commit(repo, "docs/guide.md", "How to build the synthetic project\n")

    assert _push(repo) is None
    assert _push(repo, f"git -C {repo} push origin main") is None


def test_a_push_whose_outgoing_commits_cannot_be_told_is_refused(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(tmp_path, "init", "-q", "--bare", "other.git")
    _git(repo, "remote", "add", "other", str(tmp_path / "other.git"))

    refused = _push(repo, "git push other main")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends cannot be checked "
                                         "(no remote-tracking ref of other)")
    assert "push from a job without a personal document, or remove the document from the commits" in \
        refused["message"]


def test_a_push_from_a_job_without_a_mark_is_not_checked(repo):
    _commit(repo, "private/contract.txt", f"주민등록번호 {NUMBER}\n")

    assert _push(repo, session="push-unmarked") is None
