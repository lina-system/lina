"""Read-only inventory and routing contract for lina#826."""

import argparse
import json
import sqlite3
import time
from contextlib import closing

import pytest

from hermes_cli.subcommands.migrate import build_migrate_parser


def invoke(*args):
    parser = argparse.ArgumentParser()
    build_migrate_parser(parser.add_subparsers())
    parsed = parser.parse_args(["migrate", "skills", *map(str, args)])
    with pytest.raises(SystemExit) as result:
        parsed.func(parsed)
    return result.value.code


def home(path):
    (path / "skills").mkdir(parents=True)
    with closing(sqlite3.connect(path / "state.db")) as db:
        db.execute("CREATE TABLE messages(role TEXT, timestamp REAL, tool_calls TEXT)")
        call = {"function": {"name": "skill_view", "arguments": '{"name":"tiles","file_path":"fixture.txt"}'}}
        db.execute("INSERT INTO messages VALUES ('assistant', ?, ?)", (time.time() - 1, json.dumps([call])))
        db.commit()
    return path


def skill(directory, name, description=""):
    directory.mkdir(parents=True)
    (directory / "SKILL.md").write_text(f"---\nname: {name}\ndescription: {description}\n---\n")


def test_inventory_retains_distinct_packages_and_routes_only_to_owners(tmp_path, capsys):
    left, right = (home(tmp_path / label) for label in ("h0", "h1"))
    for path in (left, right):
        skill(path / "skills" / "group" / "layout", "tiles")
        skill(path / "skills" / "group" / "repeat", "repeat")
    (right / "skills/group/layout/note.txt").write_bytes(b"\x00")
    skill(left / "skills/unique", "unique", "Fold two squares clockwise")
    skill(tmp_path / "shared/crease", "crease", "Fold two squares diagonally")
    skill(tmp_path / "bundle/spare", "spare")
    args = ["--home", f"h0={left}", "--home", f"h1={right}",
            "--shared", tmp_path / "shared", "--bundle", tmp_path / "bundle"]

    assert invoke(*args) == 0
    manifest = json.loads(capsys.readouterr().out)
    rows = manifest["records"]
    physical = {p.parent for p in tmp_path.rglob("SKILL.md")}
    assert {loc["path"] for row in rows for loc in row["locations"]} == {str(p) for p in physical}
    assert manifest["counts"]["locations"] == len(physical)
    assert manifest["counts"]["names"] == 5
    tiles = [r for r in rows if r["name"] == "tiles"]
    assert sorted(r["owners"] for r in tiles) == [["h0"], ["h1"]]
    assert len({r["sha256"] for r in tiles}) == 2
    assert all(r["layer"] == "common" and not r["copies_identical"] for r in tiles)
    assert all((r["calls_30d"], r["file_calls_30d"], r["archive_candidate"]) == (2, 2, False) for r in tiles)
    by_name = {r["name"]: r for r in rows}
    assert by_name["repeat"]["owners"] == ["h0", "h1"]
    assert by_name["repeat"]["archive_candidate"] and by_name["repeat"]["copies_identical"]
    assert by_name["unique"]["layer"] == "agent" and by_name["unique"]["owners"] == ["h0"]
    assert by_name["unique"]["weak_overlap"] == ["crease"] and not by_name["unique"]["archive_candidate"]
    assert by_name["crease"]["layer"] == "common" and not by_name["crease"]["archive_candidate"]
    assert by_name["spare"]["owners"] == [] and by_name["spare"]["layer"] == "agent"

    exclude = tmp_path / "exclude.txt"
    exclude.write_text("repeat\nabsent\n")
    output = tmp_path / "manifest.json"
    assert invoke(*args, "--exclude", exclude, "--manifest", output) == 0
    selected = json.loads(output.read_text())
    assert selected["exclusions"] == ["absent", "repeat"]
    assert {r["name"] for r in selected["records"]} == by_name.keys() - {"repeat"}
    assert len(list(tmp_path.rglob("SKILL.md"))) == len(physical)


def test_refuses_output_collisions_and_incomplete_session_evidence(tmp_path):
    source = home(tmp_path / "source")
    args = ["--home", f"h0={source}"]
    existing = tmp_path / "existing"
    existing.write_bytes(b"retain")
    assert invoke(*args, "--manifest", existing) != 0
    assert existing.read_bytes() == b"retain"
    inside = source / "skills" / "new.json"
    assert invoke(*args, "--manifest", inside) != 0
    assert not inside.exists()
    (source / "state.db").unlink()
    assert invoke(*args, "--manifest", tmp_path / "inventory.json") != 0
    assert not (tmp_path / "inventory.json").exists()


def test_multiple_views_of_one_tree_are_one_place(tmp_path, capsys):
    homes = [home(tmp_path / f"h{i}") for i in range(3)]
    tree = tmp_path / "extra"
    skill(tree / "paper", "paper")
    args = [part for i, path in enumerate(homes) for part in ("--home", f"h{i}={path}")]
    assert invoke(*args, "--home-tree", f"h0={tree}", "--home-tree", f"h2={tree}") == 0
    manifest = json.loads(capsys.readouterr().out)
    row, = manifest["records"]
    assert row["owners"] == ["h0", "h2"] and row["layer"] == "agent"
    assert manifest["counts"]["locations"] == 1
    assert row["calls_30d"] == 0 and not row["archive_candidate"]
