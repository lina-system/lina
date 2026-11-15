"""lina#442: `lina brain import` and `reindex` of a wiki directory: no file lost or changed, links become edges,
merges carry aliases and re-point edges, and the index rebuilt from the files is the same index.

The store tests run on the real Postgres from tests/_fixtures/memory_store.py. The wiki is synthetic.
"""

from __future__ import annotations

import argparse
import hashlib
import os
import unicodedata
from datetime import date
from pathlib import Path

import psycopg
import pytest

from hermes_cli.brain.resolver import DEFAULT_MAPPING, mapping_from
from hermes_cli.brain.scan import apply_merges, scan
from hermes_cli.brain.store import root_of, snapshot
from hermes_cli.subcommands.brain import build_brain_parser

PAGES = {
    "people/미나.md": "---\ntype: 사람\naliases: [Mina]\nstatus: active\n---\n# 미나\n\nProducer. Works with [[하늘]].\n",
    "people/하늘.md": "# 하늘\n\n[[미나]]의 동료. Mixes for [[Sample Label]]과 [[샘플 앨범]].\n",
    "people/준호.md": "# 준호\n\nMet [[미나의]] team; see `[[not a link]]` and [web](https://example.com/x.md).\n",
    unicodedata.normalize("NFD", "people/서연.md"): "# 서연\n\nReads [mixing](../concepts/mixing.md).\n",
    "people/dana.md": "---\ntitle: Dana\naliases: [D]\n---\nWorks at [[sample label]].\n",
    "people/eun.md": "# Eun\n\nNo links here.\n",
    "organizations/Sample Label.md": "# Sample Label\n\nPublishes [[샘플 앨범]].\n",
    "organizations/Studio One.md": "# Studio One\n\nHosts sessions.\n",
    "organizations/Print Co.md": "# Print Co\n\nPrints covers.\n",
    "projects/샘플 앨범.md": "---\naliases: [Sample Album]\ndeadline: 2026-12-01\n---\n# 샘플 앨범\n\nSummary.\n\n---\n\n"
                         "## Timeline\n\n- 2026-03-04 | owner | messages:room-1 | kickoff with [[미나]]\n"
                         "- **2026-03-09** mix review\n",
    "projects/260304 kickoff.md": "# Kickoff\n\nNotes for [[Sample Album]].\n",
    "projects/website.md": "# Website\n\nNothing yet.\n",
    "programs/finance.md": "# Finance\n\nMonthly.\n",
    "programs/health.md": "# Health\n\nWeekly.\n",
    "concepts/mixing.md": "# Mixing\n\nSee [[mastering]].\n",
    "concepts/mastering.md": "# Mastering\n\nAfter [[mixing]].\n",
    "concepts/notes.md": "# Notes\n\nConcept notes.\n",
    "concepts/loudness.md": "# Loudness\n\nStandalone.\n",
    "ideas/notes.md": "# Notes\n\nIdea notes.\n",
    "ideas/tour.md": "# Tour\n\nMaybe with [[Studio One]].\n",
    "sources/251120 interview.md": "# Interview\n\nWith [[하늘]].\n",
    "sources/article.md": "# Article\n\nAbout [[loudness]].\n",
    "sources/2026-01-05 talk.md": "# Talk\n\nGiven at [[Studio One]].\n",
    "sources/podcast.md": "# Podcast\n\nEpisode.\n",
    "meetings/260310 review.md": "# Review\n\nWith [[Dana]].\n",
    "meetings/weekly.md": "# Weekly\n\nStanding.\n",
    "writing/essay.md": "# Essay\n\nOn [[mixing]].\n",
    "writing/draft.md": "# Draft\n\nUnfinished.\n",
    "misc notes/scratch.md": "# Scratch\n\nUnsorted.\n",
    "misc notes/later.md": "# Later\n\nUnsorted too.\n",
    "home.md": "# Home\n\nStart at [[미나]].\n",
    "people/broken.md": "---\n: [unclosed\n---\nBody.\n",
}
SKIPPED = {
    "index.md": "metafile",
    "README.md": "metafile",
    "projects/SCHEMA.md": "metafile",
    "people/미나_Conflict.md": "sync conflict copy",
    "people/하늘 (conflicted copy 2026-01-01).md": "sync conflict copy",
    "@eaDir/people/미나.md": "sync artifact folder",
    "#recycle/old page.md": "sync artifact folder",
    ".git/HEAD": "git folder",
    ".git/objects/ab": "git folder",
    ".obsidian/workspace.json": "hidden folder",
    ".DS_Store": "hidden file",
    "people/photo.png": "not markdown",
}


def make_wiki(root: Path) -> Path:
    for rel, text in {**PAGES, **{path: f"artifact {path}\n" for path in SKIPPED}}.items():
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(text.encode("utf-8"))
    return root


def _state(root: Path) -> dict[str, tuple[str, int]]:
    return {str(path.relative_to(root)): (hashlib.sha256(path.read_bytes()).hexdigest(), path.stat().st_mtime_ns)
            for path in root.rglob("*") if path.is_file()}


def _slug_of(index, rel: str) -> str:
    return next(slug for slug, record in index.pages.items() if record.path == unicodedata.normalize("NFC", rel))


def test_every_file_is_a_page_or_skipped_with_its_reason_and_none_is_changed(tmp_path):
    root = make_wiki(tmp_path / "wiki")
    before = _state(root)

    index = scan(root, mapping_from(DEFAULT_MAPPING))

    assert _state(root) == before
    assert index.files_in == len(PAGES) + len(SKIPPED) >= 30
    assert dict(index.skipped) == SKIPPED
    assert sorted(record.path for record in index.pages.values()) == sorted(
        unicodedata.normalize("NFC", rel) for rel in PAGES)


def test_types_come_from_folders_and_unknown_folders_are_concepts_and_reported(tmp_path):
    index = scan(make_wiki(tmp_path / "wiki"), mapping_from(DEFAULT_MAPPING))
    types = {record.path: record.type for record in index.pages.values()}

    assert types["people/미나.md"] == "person"
    assert types["organizations/Studio One.md"] == "organization"
    assert types["programs/finance.md"] == "program"
    assert types["meetings/weekly.md"] == "meeting"
    assert types["misc notes/scratch.md"] == "concept"
    assert index.unmapped_folders == {"misc notes": 2}
    owner = mapping_from({"default": "idea", "folders": {"misc notes": "writing", "people": "organization"}})
    assert {r.path: r.type for r in scan(tmp_path / "wiki", owner).pages.values()}["misc notes/later.md"] == "writing"


def test_yymmdd_file_names_are_dates_and_ledger_keys_are_not_indexed(tmp_path):
    index = scan(make_wiki(tmp_path / "wiki"), mapping_from(DEFAULT_MAPPING))
    by_path = {record.path: record for record in index.pages.values()}

    assert by_path["projects/260304 kickoff.md"].dated == date(2026, 3, 4)
    assert by_path["sources/251120 interview.md"].dated == date(2025, 11, 20)
    assert by_path["sources/2026-01-05 talk.md"].dated == date(2026, 1, 5)
    assert by_path["concepts/mixing.md"].dated is None
    assert "status" not in by_path["people/미나.md"].frontmatter
    assert "deadline" not in by_path["projects/샘플 앨범.md"].frontmatter
    assert index.summary()["pages_with_ledger_keys_in_file"] == 2


def test_links_become_edges_and_a_particle_is_not_part_of_the_name(tmp_path):
    index = scan(make_wiki(tmp_path / "wiki"), mapping_from(DEFAULT_MAPPING))
    mina, haneul = _slug_of(index, "people/미나.md"), _slug_of(index, "people/하늘.md")
    junho, seoyeon = _slug_of(index, "people/준호.md"), _slug_of(index, "people/서연.md")
    targets = {(a, b) for a, b, _, _ in index.edges}

    assert (haneul, mina) in targets
    assert (haneul, _slug_of(index, "projects/샘플 앨범.md")) in targets
    assert (haneul, _slug_of(index, "organizations/Sample Label.md")) in targets
    assert (junho, mina) in targets
    assert {b for a, b in targets if a == junho} == {mina}
    assert (seoyeon, _slug_of(index, "concepts/mixing.md")) in targets
    assert (_slug_of(index, "projects/260304 kickoff.md"), _slug_of(index, "projects/샘플 앨범.md")) in targets
    assert index.links.by_particle == 1
    assert _slug_of(index, "people/eun.md") in index.orphans()
    assert mina == "미나" and unicodedata.is_normalized("NFC", seoyeon)
    assert _slug_of(index, "concepts/notes.md") != _slug_of(index, "ideas/notes.md")


def test_a_merge_carries_aliases_over_and_re_points_incoming_edges(tmp_path):
    index = scan(make_wiki(tmp_path / "wiki"), mapping_from(DEFAULT_MAPPING))
    mina, dana = _slug_of(index, "people/미나.md"), _slug_of(index, "people/dana.md")
    pointing_at_dana = {a for a, b, _, _ in index.edges if b == dana}
    assert pointing_at_dana

    apply_merges(index, [(dana, mina)])

    assert dana not in index.pages and index.redirects == {dana: mina}
    assert ("d", mina, "D", "merge") in index.aliases
    assert not any(dana in edge[:2] for edge in index.edges)
    assert {(a, mina) for a in pointing_at_dana} <= {(a, b) for a, b, _, _ in index.edges}


def _brain(capsys, *argv: str) -> tuple[int, str]:
    parser = argparse.ArgumentParser()
    build_brain_parser(parser.add_subparsers())
    args = parser.parse_args(["brain", *argv])
    code = args.func(args)
    return code, capsys.readouterr().out


def _rows(memory_store, root: Path) -> dict:
    with psycopg.connect(memory_store.superuser_dsn()) as conn:
        found = root_of(conn, unicodedata.normalize("NFC", str(root.resolve())))
        assert found is not None
        return snapshot(conn, found[0])


@pytest.fixture
def wiki(tmp_path):
    return make_wiki(tmp_path / "wiki")


def test_import_writes_the_index_and_a_second_import_changes_nothing(_memory_store, capsys, wiki):
    before = _state(wiki)

    code, first = _brain(capsys, "import", str(wiki))
    assert code == 0, first
    rows = _rows(_memory_store, wiki)
    index = scan(wiki, mapping_from(DEFAULT_MAPPING))
    assert set(rows["pages"]) == set(index.pages)
    assert rows["edges"] == index.edges and rows["aliases"] == index.aliases
    assert len(rows["timeline"]) == 2
    assert f"{len(PAGES) + len(SKIPPED)} files in, {len(PAGES)} pages out" in first

    code, second = _brain(capsys, "import", str(wiki))
    assert code == 0 and "Changes: none." in second
    assert _rows(_memory_store, wiki) == rows
    assert _state(wiki) == before


def test_reindex_drops_and_rebuilds_the_same_index(_memory_store, capsys, wiki):
    assert _brain(capsys, "import", str(wiki))[0] == 0
    imported = _rows(_memory_store, wiki)
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        timeline_ids = {row[0] for row in conn.execute("SELECT entry_id FROM brain.timeline")}

    code, out = _brain(capsys, "reindex", str(wiki))

    assert code == 0 and "every row written again" in out
    assert _rows(_memory_store, wiki) == imported
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        assert not timeline_ids & {row[0] for row in conn.execute("SELECT entry_id FROM brain.timeline")}


def test_a_recorded_merge_survives_reindex_and_follows_edits_to_the_files(_memory_store, capsys, wiki):
    assert _brain(capsys, "import", str(wiki))[0] == 0

    code, out = _brain(capsys, "merge", "dana", "미나", "--reason", "same person")

    assert code == 0, out
    merged = _rows(_memory_store, wiki)
    assert "dana" not in merged["pages"]
    assert ("d", "미나", "D", "merge") in merged["aliases"]
    review = next(slug for slug, row in merged["pages"].items() if row[2] == "meetings/260310 review.md")
    assert (review, "미나", "mentions", "meetings/260310 review.md") in merged["edges"]
    assert not any("dana" in edge[:2] for edge in merged["edges"])
    with psycopg.connect(_memory_store.superuser_dsn()) as conn:
        assert conn.execute("SELECT from_slug, into_slug, reason FROM brain.merges").fetchall() == [
            ("dana", "미나", "same person")]

    assert _brain(capsys, "reindex", str(wiki))[0] == 0
    assert _rows(_memory_store, wiki) == merged
    (wiki / "writing" / "draft.md").write_text("# Draft\n\nFor [[D]].\n", encoding="utf-8")
    assert _brain(capsys, "import", str(wiki))[0] == 0
    assert ("draft", "미나", "mentions", "writing/draft.md") in _rows(_memory_store, wiki)["edges"]


def test_an_edited_or_removed_file_changes_only_its_rows(_memory_store, capsys, wiki):
    assert _brain(capsys, "import", str(wiki))[0] == 0
    (wiki / "concepts" / "loudness.md").write_text("# Loudness\n\nSee [[mixing]].\n", encoding="utf-8")
    os.remove(wiki / "ideas" / "tour.md")

    code, out = _brain(capsys, "import", str(wiki))

    assert code == 0
    assert "Changes: pages ~1 -1, aliases -1, edges +1 -1." in out
    rows = _rows(_memory_store, wiki)
    assert "tour" not in rows["pages"] and ("loudness", "mixing", "mentions", "concepts/loudness.md") in rows["edges"]
