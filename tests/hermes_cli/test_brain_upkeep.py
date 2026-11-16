"""lina#443: deterministic, read-only file upkeep and durable inferred edges."""

from __future__ import annotations

import argparse
import hashlib
import json
import unicodedata
from pathlib import Path

import psycopg
import pytest

from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import scan
from hermes_cli.brain.store import root_of, snapshot
from hermes_cli.subcommands.brain import build_brain_parser


def _wiki(root: Path, pages: dict[str, str]) -> Path:
    for name, body in pages.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(body, encoding="utf-8")
    return root


def _brain(capsys, *argv: str) -> str:
    parser = argparse.ArgumentParser()
    build_brain_parser(parser.add_subparsers())
    args = parser.parse_args(["brain", *argv])
    assert args.func(args) == 0
    return capsys.readouterr().out


@pytest.mark.parametrize("suffix", [
    "은", "는", "이", "가", "을", "를", "의", "와", "과", "에", "에서", "으로", "로", "도", "만", "께", "에게",
    "입니다", "이에요", "예요", "이세요", "이군요", "에게도", "에서는",
])
def test_plain_names_with_suffixes_are_edges_but_longer_words_are_not(tmp_path, suffix):
    from hermes_cli.brain.upkeep import analyze

    root = _wiki(tmp_path / "wiki", {
        "미나.md": "---\ntitle: 미나\naliases: [별빛]\n---\n# 미나\n",
        "source.md": f"# Source\n미나{suffix} 별빛{suffix}.\n",
        "control.md": "# Control\n미나리 꽃미나 미나리는 별빛나라.\n",
        "code.md": "# Code\n`미나` [[미나]] [미나](https://example.com)\n```\n별빛\n```\n",
    })
    report = analyze(scan(root, mapping_from(DEFAULT_MAPPING)))

    assert report.inferred == {("source", "미나", "mentions", "source.md")}
    assert report.written == {("code", "미나", "mentions", "code.md")}


def test_mentions_use_titles_aliases_nfc_and_longest_names(tmp_path):
    from hermes_cli.brain.upkeep import analyze

    root = _wiki(tmp_path / "wiki", {
        "one.md": "---\ntitle: 작은 별\naliases: [푸른빛]\n---\n# Small\n",
        "two.md": "---\ntitle: 작은 별 연구\n---\n# Research\n",
        "source.md": "# Source\n작은 별 연구는 " + unicodedata.normalize("NFD", "푸른빛") + "입니다.\n",
    })
    report = analyze(scan(root, mapping_from(DEFAULT_MAPPING)))

    assert report.inferred == {("source", "one", "mentions", "source.md"),
                               ("source", "two", "mentions", "source.md")}


def test_import_and_upkeep_share_conflict_directory_exclusions(tmp_path):
    from hermes_cli.brain.upkeep import analyze

    root = _wiki(tmp_path / "wiki", {
        "clean.md": "# Clean\n",
        "notes (conflicted copy)/ghost.md": "# Ghost\n",
        "@eaDir/hidden.md": "# Hidden\n",
        "#recycle/deleted.md": "# Deleted\n",
        "note.sync-conflict-20261115.md": "# Conflict\n",
    })
    index = scan(root, mapping_from(DEFAULT_MAPPING))

    assert set(index.pages) == {"clean"}
    assert len(index.skipped) == 4
    assert analyze(index).orphans == ["clean"]


def test_upkeep_repairs_written_backlinks_and_repeats_without_file_writes(_memory_store, tmp_path, capsys):
    root = _wiki(tmp_path / "wiki", {
        "alpha.md": "# Alpha\n[[beta]]\n",
        "beta.md": "# Beta\n[[alpha]]\n",
        "target.md": "---\ntitle: 별빛\n---\n# Target\n",
        "source.md": "# Source\n별빛에게도.\n",
        "alone.md": "# Alone\n",
        "broken.md": "No title. [[absent]]\n",
    })
    before = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in root.glob("*.md")}
    _brain(capsys, "import", str(root))
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        conn.execute("DELETE FROM brain.edges WHERE from_slug = 'beta'")

    first = json.loads(_brain(capsys, "upkeep", "--root", str(root), "--json"))
    second_text = _brain(capsys, "upkeep", "--root", str(root), "--json")
    second = json.loads(second_text)

    assert first["new_links"] == 2
    assert first["fixed_backlinks"] == 1
    assert second["new_links"] == second["fixed_backlinks"] == 0
    assert first["lint"] == second["lint"]
    assert first["orphans"] == second["orphans"] == ["alone", "broken"]
    assert second_text == _brain(capsys, "upkeep", "--root", str(root), "--json")
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        rows = snapshot(conn, root_of(conn, str(root.resolve()))[0])
    assert ("source", "target", "mentions", "source.md") in rows["edges"]
    assert ("beta", "alpha", "mentions", "beta.md") in rows["edges"]
    assert ("target", "source", "mentions", "target.md") not in rows["edges"]
    assert {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in root.glob("*.md")} == before


def test_upkeep_defaults_to_all_imported_roots_and_preserves_other_edges(_memory_store, tmp_path, capsys):
    first = _wiki(tmp_path / "one", {"first.md": "# First\nsecond.\n", "second.md": "# Second\n"})
    other = _wiki(tmp_path / "two", {"third.md": "# Third\nfourth.\n", "fourth.md": "# Fourth\n",
                                    "incoming.md": "# Incoming\n"})
    for root in (first, other):
        _brain(capsys, "import", str(root))
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        conn.execute("INSERT INTO brain.edges (from_slug, to_slug, type, source_ref) "
                     "VALUES ('first', 'incoming', 'related', 'manual')")

    scoped = json.loads(_brain(capsys, "upkeep", "--root", str(first), "--json"))
    assert scoped["new_links"] == 1
    all_roots = json.loads(_brain(capsys, "upkeep", "--json"))
    assert all_roots["new_links"] == 1
    assert all_roots["roots"][1]["orphans"] == []
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        assert conn.execute("SELECT count(*) FROM brain.edges WHERE type = 'related'").fetchone()[0] == 1
