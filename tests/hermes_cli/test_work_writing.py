"""Writing refusals, warnings, field contracts, and guides (lina#36)."""

from pathlib import Path

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.mark.parametrize("text", ["주문 #19 점검", "OT-EXAMPLE 정리", "T12 마무리", "알림 점검 --dry", "알림 점검(급함)", "가" * 26])
def test_title_refuses_when_internal_details_are_present(tmp_path, monkeypatch, text):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    code, _, _ = work.run("edit", "T1", "--title", text)
    # Then
    assert code == 2


def test_warning_allows_write_when_only_english_word_is_unlisted(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    code, _, error = work.run("edit", "T1", "--title", "breaker 다시 점검")
    # Then
    assert code == 0
    assert "breaker" in error


@pytest.mark.parametrize("locale", ["ko", "en"])
def test_writing_guide_equals_shipped_document_when_rendered(tmp_path, monkeypatch, locale):
    # Given
    work = WorkEnv(tmp_path, monkeypatch, locale=locale)
    document = Path(__file__).resolve().parents[2] / "docs" / "work" / f"writing-guide.{locale}.md"
    # When
    output = work.ok("fields", "--markdown")
    # Then
    assert output == document.read_text(encoding="utf-8-sig")


def test_fields_version_matches_json_when_reading_specification(tmp_path, monkeypatch):
    # Given
    import json
    work = WorkEnv(tmp_path, monkeypatch)
    # When
    spec = json.loads(work.ok("fields", "--json"))
    # Then
    assert spec["version"] == work.json()["fields"]
    assert spec["fields"]


def test_missing_why_refuses_new_todo(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    # When
    code, _, error = work.run("add", "할 일", "--by", "alpha", "--goal", "G1", "--title", "자료 정리",
                              "--done", "자료가 정리된다", "--check", "false", "--omo", "next")
    # Then
    assert code == 2
    assert "--why" in error


def test_lint_does_not_change_records_when_diagnostics_are_requested(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    work.ok("note", "T1", "#19 고쳤다")
    before = work.ok("export-jsonl")
    # When
    work.ok("lint", "--open")
    # Then
    assert work.ok("export-jsonl") == before


@pytest.mark.parametrize("text,code", [("알림 점검", 0), ("#19 정리", 1)])
def test_fields_check_returns_validation_status(tmp_path, monkeypatch, text, code):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    # When
    result, _, _ = work.run("fields", "할 일 제목", "--check", text)
    # Then
    assert result == code
