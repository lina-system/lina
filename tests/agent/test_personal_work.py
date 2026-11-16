"""lina#308: what a job derives from a personal document lives in the job's two folders, and those folders are gone
when the job ends, by success, failure or cancel, with the count in the job's run record. Nothing beside them is
touched. The run record is the real store of tests/_fixtures/memory_store.py."""

from __future__ import annotations

import asyncio
import tempfile
from pathlib import Path

import pytest

from agent import personal_work
from hermes_state_personal import PersonalMarks
from hermes_state_store import MessageStore


@pytest.fixture
def marks(tmp_path, monkeypatch):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "home"))
    store = MessageStore("personal-test")
    yield PersonalMarks(store)
    store.close()


def _derive(folders: personal_work.JobFolders) -> None:
    (folders.work / "pages").mkdir()
    (folders.work / "pages" / "page-1.jpg").write_bytes(b"synthetic page")
    (folders.work / "extract.txt").write_text("synthetic extract", encoding="utf-8")
    (folders.temp / "copy.pdf").write_bytes(b"synthetic copy")


def _neighbours(job_id: str) -> list[Path]:
    """A sibling job's work folder and a temp folder that only shares the prefix: never the job's own."""
    other = personal_work.folders_for(f"{job_id}-other").work
    lookalike = Path(tempfile.gettempdir()) / f"{personal_work.TEMP_PREFIX}{job_id}"
    for folder in (other, lookalike):
        folder.mkdir(parents=True, exist_ok=True)
        (folder / "keep.txt").write_text("not derived from this job", encoding="utf-8")
    return [other / "keep.txt", lookalike / "keep.txt"]


def _cleaned(marks: PersonalMarks, job_id: str) -> list[dict]:
    return [event for event in marks.events(job_id) if event["kind"] == "cleaned"]


def test_the_job_folders_are_gone_after_success(marks):
    kept = _neighbours("job-ok")
    with personal_work.job_folders(marks, "job-ok") as folders:
        _derive(folders)
    assert not folders.work.exists() and not folders.temp.exists()
    assert all(path.exists() for path in kept)
    assert [(e["deleted"], e["outcome"]) for e in _cleaned(marks, "job-ok")] == [(3, "success")]


def test_the_job_folders_are_gone_after_failure(marks):
    with pytest.raises(RuntimeError, match="extraction failed"):
        with personal_work.job_folders(marks, "job-failed") as folders:
            _derive(folders)
            raise RuntimeError("extraction failed")
    assert not folders.work.exists() and not folders.temp.exists()
    assert [(e["deleted"], e["outcome"]) for e in _cleaned(marks, "job-failed")] == [(3, "failure")]


@pytest.mark.parametrize("cancel", [KeyboardInterrupt, asyncio.CancelledError])
def test_the_job_folders_are_gone_after_cancel(marks, cancel):
    with pytest.raises(cancel):
        with personal_work.job_folders(marks, "job-cancelled") as folders:
            _derive(folders)
            raise cancel()
    assert not folders.work.exists() and not folders.temp.exists()
    assert [(e["deleted"], e["outcome"]) for e in _cleaned(marks, "job-cancelled")] == [(3, "cancelled")]


def test_ending_a_job_without_folders_deletes_nothing_and_records_nothing(marks):
    assert personal_work.end_job(marks, "job-never-opened", "session_end") == 0
    assert marks.events("job-never-opened") == []


def test_a_document_under_a_marker_folder_is_personal(tmp_path):
    inbox = tmp_path / "inbox"
    (inbox / "tax" / "2026").mkdir(parents=True)
    (inbox / "tax" / personal_work.MARKER_FILE).write_text("", encoding="utf-8")
    document = inbox / "tax" / "2026" / "return.pdf"
    document.write_bytes(b"synthetic")
    assert personal_work.marker_folder(str(document)) == (inbox / "tax").resolve()
    assert personal_work.marker_folder(str(inbox / "elsewhere.pdf")) is None
