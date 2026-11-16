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


def test_a_push_whose_added_line_quotes_text_of_a_personal_document_is_refused(repo, tmp_path):
    sentence = "The tenant pays a monthly rent of seven hundred synthetic dollars."
    document = tmp_path / "elsewhere" / "lease.txt"
    document.parent.mkdir()
    document.write_text(f"Synthetic lease\n{sentence}\n", encoding="utf-8")
    _mark(document)
    _commit(repo, "README.md", f"synthetic project\n\n> {sentence}\n")

    refused = _push(repo)

    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry text from a "
                                         "personal document")


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


def _documents_branch(repo: Path) -> Path:
    _git(repo, "checkout", "-q", "-b", "documents")
    document = _commit(repo, "private/contract.txt", "Synthetic lease contract for a test tenant\n")
    _git(repo, "checkout", "-q", "main")
    return document


def _commit_on(repo: Path, branch: str, name: str, text: str) -> Path:
    _git(repo, "checkout", "-q", branch)
    path = _commit(repo, name, text)
    _git(repo, "checkout", "-q", "main")
    return path


def test_a_push_by_the_remote_s_push_refspec_of_a_branch_with_a_personal_document_is_refused(repo):
    _mark(_documents_branch(repo))
    _git(repo, "config", "remote.origin.push", "documents:documents")

    refused = _push(repo, "git push origin")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry the personal "
                                         "document ")
    assert _push(repo, "git push")["action"] == "block"


def test_a_push_by_the_remote_s_push_refspec_of_a_clean_branch_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "checkout", "-q", "-b", "documents")
    _commit(repo, "docs/guide.md", "How to build the synthetic project\n")
    _git(repo, "checkout", "-q", "main")
    _git(repo, "config", "remote.origin.push", "documents:documents")

    assert _push(repo, "git push origin") is None


def test_a_matching_push_default_inspects_every_branch(repo):
    _mark(_documents_branch(repo))

    assert _push(repo, "git -c push.default=matching push origin")["action"] == "block"
    _git(repo, "config", "push.default", "matching")
    assert _push(repo, "git push origin")["action"] == "block"


def test_a_colon_refspec_inspects_every_branch_like_a_matching_push(repo):
    _documents_branch(repo)
    _git(repo, "push", "-q", "origin", "documents")
    _mark(_commit_on(repo, "documents", "private/more.txt", "Synthetic second contract page\n"))

    for command in ("git push origin :", "git push origin +:"):
        assert _push(repo, command)["action"] == "block"
    _git(repo, "config", "remote.origin.push", ":")
    assert _push(repo, "git push origin")["action"] == "block"


def test_a_colon_refspec_with_clean_branches_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _commit(repo, "docs/guide.md", "How to build the synthetic project\n")

    assert _push(repo, "git push origin :") is None


def test_a_remote_set_to_mirror_inspects_every_ref(repo):
    _documents_branch(repo)
    _git(repo, "push", "-q", "origin", "documents")
    _mark(_commit_on(repo, "documents", "private/more.txt", "Synthetic second contract page\n"))
    _git(repo, "config", "remote.origin.mirror", "true")

    assert _push(repo, "git push origin")["action"] == "block"
    _git(repo, "config", "remote.origin.mirror", "false")
    assert _push(repo, "git push origin") is None
    assert _push(repo, "git -c remote.origin.mirror=true push origin")["action"] == "block"


def test_a_mirror_remote_with_clean_refs_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _commit(repo, "docs/guide.md", "How to build the synthetic project\n")
    _git(repo, "config", "remote.origin.mirror", "true")

    assert _push(repo, "git push origin") is None


def _empty_commit(repo: Path, message: str) -> None:
    _git(repo, "commit", "-q", "--allow-empty", "-m", message)


def test_an_outgoing_commit_message_with_an_id_number_is_refused(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _empty_commit(repo, f"Contract ID {NUMBER}")

    refused = _push(repo, "git push origin main")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry an ID or account "
                                         "number")


def test_an_outgoing_commit_message_with_personal_text_is_refused(repo, tmp_path):
    document = tmp_path / "private" / "contract.txt"
    document.parent.mkdir()
    document.write_text("Synthetic lease between the test tenant and the test landlord\n", encoding="utf-8")
    _mark(document)
    _empty_commit(repo, "Synthetic lease between the test tenant and the test landlord")

    refused = _push(repo, "git push origin main")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends carry text from a "
                                         "personal document")


def test_a_clean_commit_message_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _empty_commit(repo, "Describe the build in the guide")

    assert _push(repo, "git push origin main") is None


def test_an_annotated_tag_message_with_an_id_number_is_refused(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "tag", "-a", "v1", "-m", f"Release for ID {NUMBER}")  # on a commit the remote already has

    for command in ("git push origin v1", "git push origin refs/tags/v1", "git push origin --tags"):
        refused = _push(repo, command)
        assert refused["message"].startswith("Refused (public: git push): the commits it sends carry an ID or "
                                             "account number"), command


def test_a_clean_annotated_tag_is_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "tag", "-a", "v1", "-m", "Release of the synthetic project")

    assert _push(repo, "git push origin v1") is None
    assert _push(repo, "git push origin --tags") is None


_TAG_REFUSAL = "Refused (public: git push): the commits it sends carry an ID or account number"


def test_follow_tags_reads_the_annotated_tags_it_would_send(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "tag", "-a", "v1", "-m", f"Release for ID {NUMBER}")  # on a commit the remote already has

    assert _push(repo, "git push origin main --follow-tags")["message"].startswith(_TAG_REFUSAL)
    assert _push(repo, "git push origin main") is None
    assert _push(repo, "git push origin main --follow-tags --no-follow-tags") is None
    assert _push(repo, "git -c push.followTags=true push origin main")["message"].startswith(_TAG_REFUSAL)
    _git(repo, "config", "push.followTags", "true")
    assert _push(repo, "git push origin main")["message"].startswith(_TAG_REFUSAL)
    assert _push(repo, "git push origin main --no-follow-tags") is None


def test_follow_tags_skips_a_tag_the_pushed_refs_do_not_reach(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "checkout", "-q", "-b", "side")
    _empty_commit(repo, "Side work")
    _git(repo, "tag", "-a", "side-1", "-m", f"Release for ID {NUMBER}")
    _git(repo, "checkout", "-q", "main")

    assert _push(repo, "git push origin main --follow-tags") is None


def test_a_mirror_push_reads_every_annotated_tag(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "tag", "-a", "v1", "-m", f"Release for ID {NUMBER}")

    assert _push(repo, "git push origin --mirror")["message"].startswith(_TAG_REFUSAL)
    _git(repo, "config", "remote.origin.mirror", "true")
    assert _push(repo, "git push origin")["message"].startswith(_TAG_REFUSAL)


def test_follow_tags_and_mirror_with_a_clean_tag_are_allowed(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "tag", "-a", "v1", "-m", "Release of the synthetic project")

    assert _push(repo, "git push origin main --follow-tags") is None
    assert _push(repo, "git push origin --mirror") is None


def test_a_push_default_that_cannot_be_resolved_is_refused(repo, tmp_path):
    _mark(tmp_path / "elsewhere" / "id-card.pdf")
    _git(repo, "config", "push.default", "sideways")

    refused = _push(repo, "git push origin")

    assert refused["message"].startswith("Refused (public: git push): the commits it sends cannot be checked "
                                         "(error: malformed value for push.default: sideways")
    assert "push from a job without a personal document, or remove the document from the commits" in \
        refused["message"]


def test_a_push_from_a_job_without_a_mark_is_not_checked(repo):
    _commit(repo, "private/contract.txt", f"주민등록번호 {NUMBER}\n")

    assert _push(repo, session="push-unmarked") is None
