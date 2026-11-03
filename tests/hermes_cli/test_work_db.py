"""Store import/export preserves work record behavior (lina#36)."""

import json

from tests.hermes_cli._work_env import WorkEnv


def test_export_can_be_imported_without_losing_notes(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    work.goal_and_todos()
    work.ok("note", "T1", "자료를 읽었다")
    export = tmp_path / "record.jsonl"
    work.ok("export-jsonl", "--to", str(export))
    from hermes_cli.work import ledger
    before = ledger.read_all()[0]
    ledger.close()
    target = WorkEnv(tmp_path / "target", monkeypatch)
    # When
    target.ok("import", "--from", str(export))
    # Then
    after = ledger.read_all()[0]
    ledger.close()
    assert after == before
    rows = [json.loads(line) for line in export.read_text(encoding="utf-8-sig").splitlines()]
    assert any(row.get("op") == "note" and row["notes"][-1]["text"] == "자료를 읽었다" for row in rows)


def test_import_is_idempotent_when_source_is_repeated(tmp_path, monkeypatch):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    source = work.fixture()
    before = work.ok("export-jsonl")
    # When
    work.ok("import", "--from", str(source))
    # Then
    assert work.ok("export-jsonl") == before
