"""Exercise the migration through the same parser and handler as the CLI."""

import argparse
import hashlib
import json

import pytest

from agent.prompt_builder import load_common_persona
from hermes_cli.profiles import create_profile, delete_profile, rename_profile
from hermes_cli.profiles_archive import export_profile, import_profile
from hermes_cli.subcommands.migrate import build_migrate_parser
from hermes_constants import COMMON_PERSONA_FILE


def _invoke(args, capsys):
    parser = argparse.ArgumentParser()
    build_migrate_parser(parser.add_subparsers())
    parsed = parser.parse_args(["migrate", "persona", *map(str, args)])
    with pytest.raises(SystemExit) as exit_info:
        parsed.func(parsed)
    return exit_info.value.code, json.loads(capsys.readouterr().out)


def test_dry_run_then_apply_to_copies(tmp_path, capsys):
    root = tmp_path / "install"
    args = ["--home", root]
    original = {}
    for name in ("default", "one", "two"):
        source = tmp_path / f"{name}.md"
        original[source] = f"- Shared item\r\n\r\n- {name}\n\n- Remove this\n".encode()
        source.write_bytes(original[source])
        args.extend(["--source", f"{name}={source}"])
    digest = hashlib.sha256(b"- Remove this").hexdigest()
    drops = tmp_path / "drops"
    drops.write_text(f"{digest}:superseded\n", encoding="utf-8-sig")
    args.extend(["--drop", drops])
    code, preview = _invoke(args, capsys)
    assert code == 0
    assert not root.exists()
    assert len(preview["ledger"]) == 9

    for _ in range(2):
        code, applied = _invoke([*args, "--apply"], capsys)
        assert code == 0
        assert applied["ledger"] == preview["ledger"]
    for source, raw in original.items():
        name = source.stem
        home = root if name == "default" else root / "profiles" / name
        assert (home / "SOUL.md").read_bytes().strip() == f"- {name}".encode()
        assert (home / "SOUL.common.md").read_bytes() == b"- Shared item\r\n"
        ledger = [json.loads(line) for line in (home / "persona-ledger.jsonl").read_text().splitlines()]
        assert ledger == [row for row in preview["ledger"] if row["home"] == name]
        assert any(row.get("reason") == "superseded" for row in ledger)
        assert source.read_bytes() == raw


@pytest.mark.parametrize("filename", ["SOUL.md", "SOUL.common.md"])
def test_conflict_is_detected_before_any_persona_is_written(tmp_path, capsys, filename):
    source = tmp_path / "source.md"
    source.write_text("Copied paragraph\n")
    root = tmp_path / "install"
    existing = root / "profiles" / "second" / filename
    existing.parent.mkdir(parents=True)
    existing.write_text("Local edits\n")
    args = ["--home", root, "--source", f"first={source}"]
    other = tmp_path / "other.md"
    other.write_bytes(source.read_bytes())
    code, _ = _invoke([*args, "--source", f"second={other}", "--apply"], capsys)
    assert code != 0
    assert existing.read_text() == "Local edits\n"
    assert not (root / "profiles" / "first").exists()


@pytest.fixture
def migrated(tmp_path, monkeypatch, capsys):
    root = tmp_path / "install"
    args = ["--home", root, "--apply"]
    for name in ("default", "first", "second"):
        path = tmp_path / f"{name}.md"
        private = "- Local rule\n" if name == "first" else ""
        path.write_text(f"# Rules\n- First rule\n- Next rule\n{private}")
        args.extend(["--source", f"{name}={path}"])
    code, report = _invoke(args, capsys)
    assert code == 0, report
    monkeypatch.setenv("HERMES_HOME", str(root))
    monkeypatch.setenv("LINA_HOME", str(root))
    monkeypatch.setenv("HOME", str(tmp_path))
    return root


@pytest.mark.parametrize(("source", "full"), [("first", False), ("second", False), ("default", True)])
def test_clone_keeps_migrated_persona(migrated, source, full):
    home = migrated if source == "default" else migrated / "profiles" / source
    expected = "# Rules\n- First rule\n\n- Next rule\n"
    assert load_common_persona(home_override=home) == expected
    clone = create_profile("copy", clone_from=source, clone_all=full, no_alias=True)

    assert load_common_persona(home_override=clone) == expected
    assert (clone / "SOUL.md").read_bytes() == (home / "SOUL.md").read_bytes()
    assert (clone / "SOUL.common.md").read_bytes() == expected.encode()
    fresh = create_profile("fresh", no_skills=True, no_alias=True)
    assert load_common_persona(home_override=fresh) is None


def test_renamed_profile_keeps_common_persona(migrated):
    renamed = rename_profile("first", "renamed")
    assert load_common_persona(home_override=renamed) == "# Rules\n- First rule\n\n- Next rule\n"


def test_recreated_profile_has_no_deleted_persona(migrated):
    assert load_common_persona(home_override=migrated / "profiles" / "first")
    delete_profile("first", yes=True)
    fresh = create_profile("first", no_skills=True, no_alias=True)
    assert load_common_persona(home_override=fresh) is None


@pytest.mark.parametrize("source", ["first", "second", "default"])
def test_export_import_preserves_persona_in_another_install(migrated, tmp_path, monkeypatch, source):
    home = migrated if source == "default" else migrated / "profiles" / source
    persona = (home / "SOUL.md").read_bytes()
    archive = export_profile(source, str(tmp_path / "export.tar.gz"))
    destination = tmp_path / "other"
    monkeypatch.setenv("LINA_HOME", str(destination))
    monkeypatch.setenv("HERMES_HOME", str(destination))
    imported = import_profile(str(archive), "imported")
    assert (imported / "SOUL.md").read_bytes() == persona
    assert load_common_persona(home_override=imported) == "# Rules\n- First rule\n\n- Next rule\n"


def test_clone_without_shared_file_keeps_local_bytes(tmp_path, monkeypatch):
    root = tmp_path / "install"
    root.mkdir()
    monkeypatch.setenv("HERMES_HOME", str(root))
    monkeypatch.setenv("LINA_HOME", str(root))
    original = b"# Identity\r\n- Keep local spacing.  \r\n"
    (root / "SOUL.md").write_bytes(original)

    clone = create_profile("copy", clone_from="default", no_alias=True)

    assert (clone / "SOUL.md").read_bytes() == original
    assert load_common_persona(home_override=clone) is None
    assert not (root / COMMON_PERSONA_FILE).exists()
