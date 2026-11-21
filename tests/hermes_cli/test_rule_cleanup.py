import json
import os
from pathlib import Path

import pytest


def test_cleanup_without_confirmations_leaves_home_untouched(tmp_path: Path) -> None:
    from hermes_cli.rule_cleanup import cleanup

    root = tmp_path / "copy"
    root.mkdir()
    source = root / "SOUL.md"
    original = b"Always use plain language.\r\n"
    source.write_bytes(original)

    assert cleanup(root, []) is None

    assert source.read_bytes() == original
    assert sorted(root.iterdir()) == [source]


@pytest.fixture
def copy_home(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> Path:
    root = tmp_path / "copy"
    root.mkdir()
    (root / ".standing-rule-copy").touch()
    (root / "config.yaml").write_text("undo: {}\n", encoding="utf-8")
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setenv("LINA_HOME", str(root))
    monkeypatch.setenv("HERMES_HOME", str(root))
    return root


def test_committed_conversation_rule_is_removed_from_profile_and_restored(copy_home: Path) -> None:
    from hermes_cli.rule_census import candidates
    from hermes_cli.rule_cleanup import cleanup
    from hermes_cli.undo import main

    source = copy_home / "profiles/alpha/SOUL.md"
    source.parent.mkdir(parents=True)
    original = b"# Persona\r\n\r\n- Always use plain language.\r\n- Never repeat a status update.\r\n"
    source.write_bytes(original)
    selected = [item for item in candidates(copy_home, "alpha") if "plain language" in item.statement]
    assert len(selected) == 1

    change_id = cleanup(copy_home, selected)

    assert change_id is not None
    assert source.read_bytes() == b"# Persona\r\n\r\n- Never repeat a status update.\r\n"
    assert main(["apply", change_id]) == 0
    assert source.read_bytes() == original


def test_undo_restores_structured_filter_and_skill_sources(copy_home: Path) -> None:
    from hermes_cli.rule_census import candidates
    from hermes_cli.rule_cleanup import cleanup
    from hermes_cli.undo import main

    home = copy_home / "profiles/alpha"
    skill = home / "skills/fixture/SKILL.md"
    cron = home / "cron/jobs.json"
    originals = {
        skill: b"---\nname: fixture\ndescription: Fixture skill\n---\nNever repeat a status update.\nKeep using the checklist.\n",
        cron: b'{ "jobs": [{ "id": "fixture", "prompt": "Only show messages that mention me.\\nSummarize the results." }] }\n',
    }
    for path, data in originals.items():
        path.parent.mkdir(parents=True)
        path.write_bytes(data)
    selected = [
        item for item in candidates(copy_home, "alpha")
        if item.path == skill or item.kind == "filter"
    ]
    assert {item.path for item in selected} == {skill, cron}

    change_id = cleanup(copy_home, selected)

    assert change_id is not None
    assert skill.read_bytes() == originals[skill].replace(b"Never repeat a status update.\n", b"")
    assert json.loads(cron.read_bytes()) == {"jobs": [{"id": "fixture", "prompt": "Summarize the results."}]}
    assert main(["apply", change_id]) == 0
    assert {path: path.read_bytes() for path in originals} == originals


@pytest.mark.parametrize("failure", ["snapshot", "stale", "outside", "hardlink", "unmarked"])
def test_invalid_batch_preserves_all_selected_files(copy_home: Path, failure: str) -> None:
    from hermes_cli.rule_census import candidates
    from hermes_cli.rule_cleanup import cleanup
    from hermes_cli.undo.models import UndoError

    soul = copy_home / "profiles/alpha/SOUL.md"
    notes = soul.parent / "memories/USER.md"
    notes.parent.mkdir(parents=True)
    soul.write_bytes(b"Always use plain language.\n")
    notes.write_bytes(b"Never repeat a status update.\n")
    selected = candidates(copy_home, "alpha")
    assert {item.path for item in selected} == {soul, notes}
    outside = copy_home.parent / "outside"
    match failure:
        case "snapshot":
            outside.mkdir()
            (copy_home / "checkpoints").symlink_to(outside, target_is_directory=True)
        case "stale":
            notes.write_bytes(b"Never send a status update.\n")
        case "outside":
            notes.rename(outside)
            notes.symlink_to(outside)
        case "hardlink":
            os.link(notes, outside)
        case "unmarked":
            (copy_home / ".standing-rule-copy").unlink()
    expected = {path: path.read_bytes() for path in (soul, notes)}

    with pytest.raises(UndoError):
        cleanup(copy_home, selected)

    assert {path: path.read_bytes() for path in expected} == expected
    assert not (copy_home / "undo/journal.jsonl").exists()


def test_cleanup_rolls_back_if_a_later_source_cannot_be_replaced(
    copy_home: Path, monkeypatch: pytest.MonkeyPatch,
) -> None:
    from hermes_cli import rule_cleanup
    from hermes_cli.rule_census import candidates
    from hermes_cli.undo.models import UndoError
    from hermes_cli.undo.storage import atomic

    soul = copy_home / "profiles/alpha/SOUL.md"
    notes = soul.parent / "memories/USER.md"
    notes.parent.mkdir(parents=True)
    original = {soul: b"Always use plain language.\n", notes: b"Never repeat a status update.\n"}
    for path, data in original.items():
        path.write_bytes(data)
    selected = candidates(copy_home, "alpha")
    assert [item.path for item in selected] == [soul, notes]

    def fail_on_notes(path: Path, data: bytes, *, mode: int = 0o600) -> None:
        if path == notes:
            raise PermissionError("synthetic write failure")
        atomic(path, data, mode=mode)

    monkeypatch.setattr(rule_cleanup, "atomic", fail_on_notes)

    with pytest.raises(UndoError):
        rule_cleanup.cleanup(copy_home, selected)

    assert {path: path.read_bytes() for path in original} == original
    assert not (copy_home / "undo/journal.jsonl").exists()
    assert list((copy_home / "undo/pending").iterdir()) == []
