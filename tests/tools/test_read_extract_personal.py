"""lina#308: read_file's extractor never sends a personal document to hosted OCR (a third-party upload) and keeps its
temp copies in the job's temp folder; an unmarked document still uses hosted OCR. ``anydoc`` is a recording stand-in
(hosted OCR would reach the network); the marks are the real store of tests/_fixtures/memory_store.py."""

from __future__ import annotations

import io
import zipfile

import pytest

from agent import personal_work
from gateway.session_context import scoped_current_session_id
from tools import read_extract

SESSION = "read-personal"


class _NeedsOcr(Exception):
    pages = [1, 2]


class _Anydoc:
    NeedsOcrError = _NeedsOcr

    def __init__(self) -> None:
        self.hosted: list[str] = []

    def to_markdown(self, path, ocr=None, **_kwargs):
        if ocr == "hosted":
            self.hosted.append(path)
            return "hosted text"
        raise _NeedsOcr()


@pytest.fixture
def anydoc(monkeypatch, tmp_path):
    monkeypatch.setenv("HERMES_HOME", str(tmp_path / "home"))
    fake = _Anydoc()
    monkeypatch.setattr(read_extract, "_anydoc", lambda: fake)
    monkeypatch.setattr(read_extract, "_hosted_ocr_config", lambda: (True, "synthetic-key", None))
    personal_work._MARKS.clear()
    return fake


def _scan(tmp_path, name: str):
    path = tmp_path / name
    path.write_bytes(b"%PDF-1.4 synthetic scan")
    return path


def test_a_marked_scan_is_not_sent_to_hosted_ocr_and_an_unmarked_one_is(anydoc, tmp_path):
    marked, plain = _scan(tmp_path, "id-card.pdf"), _scan(tmp_path, "menu.pdf")
    with scoped_current_session_id(SESSION):
        personal_work.marks_for_home().mark(SESSION, str(marked.resolve()), "owner")
        refused = read_extract.extract_document_text(str(marked))
        used = read_extract.extract_document_text(str(plain))

    folders = personal_work.folders_for(SESSION)
    assert anydoc.hosted == [str(plain)]
    assert used == "hosted text\n"
    assert "Hosted OCR was not used: it is a personal document" in refused
    assert f"{folders.temp}/page" in refused


@pytest.mark.parametrize("folder", ["work", "temp"])
def test_a_scan_derived_in_the_job_folders_is_not_sent_to_hosted_ocr(anydoc, tmp_path, folder):
    document = _scan(tmp_path, "id-card.pdf")
    with scoped_current_session_id(SESSION):
        marks = personal_work.marks_for_home()
        job = marks.mark(SESSION, str(document.resolve()), "owner")
        derived = _scan(getattr(personal_work.open_job_folders(marks, job), folder), "scan.pdf")
        refused = read_extract.extract_document_text(str(derived))
        personal_work.end_job(marks, job, "success")
    (tmp_path / "plain").mkdir()
    elsewhere = _scan(tmp_path / "plain", "scan.pdf")
    with scoped_current_session_id("read-unmarked"):
        used = read_extract.extract_document_text(str(elsewhere))

    assert "Hosted OCR was not used: it is a personal document" in refused
    assert anydoc.hosted == [str(elsewhere)]
    assert used == "hosted text\n"


def _docx(text: str) -> bytes:
    buffer = io.BytesIO()
    with zipfile.ZipFile(buffer, "w") as zf:
        zf.writestr("word/document.xml", (
            '<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>'
            f"<w:p><w:r><w:t>{text}</w:t></w:r></w:p></w:body></w:document>"))
    return buffer.getvalue()


def test_the_temp_copy_of_a_marked_document_is_made_in_the_job_temp_folder(anydoc, tmp_path, monkeypatch):
    document = tmp_path / "lease.docx"
    seen: list[str] = []
    extractor = read_extract._STDLIB_EXTRACTORS[".docx"]
    monkeypatch.setitem(read_extract._STDLIB_EXTRACTORS, ".docx",
                        lambda path: seen.append(path) or extractor(path))
    with scoped_current_session_id(SESSION):
        personal_work.marks_for_home().mark(SESSION, str(document.resolve()), "owner")
        text = read_extract.extract_document_bytes(_docx("synthetic lease"), str(document))

    folders = personal_work.folders_for(SESSION)
    assert "synthetic lease" in text
    assert len(seen) == 1 and seen[0].startswith(f"{folders.temp}/")
    assert list(folders.temp.iterdir()) == []  # the copy itself is removed at once


def test_a_marked_scan_read_without_hosted_ocr_renders_into_the_job_temp_folder(anydoc, tmp_path, monkeypatch):
    monkeypatch.setattr(read_extract, "_hosted_ocr_config", lambda: (False, None, None))
    marked, plain = _scan(tmp_path, "id-card.pdf"), _scan(tmp_path, "menu.pdf")
    with scoped_current_session_id(SESSION):
        personal_work.marks_for_home().mark(SESSION, str(marked.resolve()), "owner")
        warning = read_extract.extract_document_text(str(marked))
        other = read_extract.extract_document_text(str(plain))

    folders = personal_work.folders_for(SESSION)
    assert f"{folders.temp}/page" in warning and "$TMPDIR/page" not in warning
    assert "$TMPDIR/page" in other
    assert anydoc.hosted == []
