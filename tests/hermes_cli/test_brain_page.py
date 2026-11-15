"""lina#442: the brain page shape round-trips byte for byte, slugs keep Korean in NFC, pages never hold status."""

from __future__ import annotations

import unicodedata
from datetime import date

import pytest

from hermes_cli.brain.page import PageError, append, new_page, page_id, parse, render, slugify

HAND_WRITTEN = (
    "---\n"
    "type: 사람\n"
    "aliases: [Mina, 미나]\n"
    "status: active\n"
    "---\n"
    "# 미나\n"
    "\n"
    "Works on the sample album.  \n"
    "\n"
    "---\n"
    "A horizontal rule, not the divider.\n"
    "\n"
    "---\n"
    "\n"
    "## Timeline\n"
    "\n"
    "- **2026-03-04** met at the studio\n"
    "  continued on a second line\n"
    "- 2026-03-09 | owner | messages:room-1 | asked for the mix\n"
    "- an undated note\n"
    "\tno final newline"
)


def test_a_hand_written_page_parses_into_its_parts_and_renders_back_unchanged():
    page = parse(HAND_WRITTEN)

    assert render(page) == HAND_WRITTEN
    assert page.frontmatter["aliases"] == ["Mina", "미나"]
    assert "A horizontal rule, not the divider." in page.compiled
    assert [(e.date, e.grade, e.source_ref) for e in page.entries] == [
        (date(2026, 3, 4), "external", None),
        (date(2026, 3, 9), "owner", "messages:room-1"),
        (None, "external", None),
    ]
    assert page.entries[0].text == "met at the studio continued on a second line"
    assert page.entries[1].text == "asked for the mix"
    assert [e.line for e in page.entries] == [17, 19, 20]


def test_a_page_the_renderer_wrote_parses_and_renders_to_the_same_bytes():
    written = append(append(new_page("person", "미나", "Summary paragraph.", aliases=("Mina",)),
                            date(2026, 3, 4), "met at the studio", grade="owner", source_ref="messages:room-1"),
                     date(2026, 3, 5), "sent the contract")
    text = render(written)

    again = parse(text)
    assert render(again) == text
    assert again.frontmatter == {"type": "person", "title": "미나", "aliases": ["Mina"]}
    assert [(e.line, e.date, e.grade, e.text) for e in again.entries] == [
        (e.line, e.date, e.grade, e.text) for e in written.entries]
    assert [e.grade for e in again.entries] == ["owner", "external"]


def test_appending_keeps_every_byte_already_written():
    before = parse(HAND_WRITTEN)
    after = render(append(before, date(2026, 3, 10), "a new line"))

    assert after.startswith(HAND_WRITTEN)
    assert parse(after).entries[-1].date == date(2026, 3, 10)


def test_a_page_never_stores_status_or_a_deadline():
    with pytest.raises(PageError):
        new_page("project", "Album", "Summary.", extra={"status": "active"})
    with pytest.raises(PageError):
        new_page("project", "Album", "Summary.", extra={"deadline": "2026-12-01"})


def test_a_korean_slug_is_kept_in_nfc_and_nfd_input_gives_the_same_slug():
    composed = "김미나 프로젝트"
    decomposed = unicodedata.normalize("NFD", composed)
    assert decomposed != composed

    assert slugify(decomposed) == slugify(composed) == "김미나-프로젝트"
    assert unicodedata.is_normalized("NFC", slugify(decomposed))
    assert page_id(slugify(decomposed)) == page_id(slugify(composed))
    assert slugify("음악·제작 / Notes") == "음악-제작-notes"
