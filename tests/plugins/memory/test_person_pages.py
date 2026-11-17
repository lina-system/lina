"""lina#445: a person's facts live on their brain page. The card a session injects is built from the page, Honcho's
conclusions about the person are written to the page once each (grade agent, source honcho:<id>), and an external
timeline line reaches the card only inside the quoted data block.

The store tests run on the real Postgres from tests/_fixtures/memory_store.py; Honcho is a fake. The wiki is
synthetic.
"""

from __future__ import annotations

import argparse
import json
from datetime import UTC, date, datetime
from pathlib import Path
from types import SimpleNamespace

import psycopg
import pytest

from hermes_cli.subcommands.brain import build_brain_parser
from plugins.memory.lina.identity import OWNER_HANDLE, IdentityRegistry

WIKI = {
    "people/mina.md": "---\ntype: person\ntitle: Mina\n---\n\nProducer at a small label. Prefers short reports.\n",
    "people/haneul.md": "# Haneul\n\nMixing engineer.\n\n---\n\n## Timeline\n\n"
                        "- 2026-02-01 heard about her from a newsletter\n",
    "people/dana.md": "# Dana\n\nDesigner.\n",
}
WORKSPACE = "lina-test"


def make_wiki(root: Path) -> Path:
    for rel, text in WIKI.items():
        (root / rel).parent.mkdir(parents=True, exist_ok=True)
        (root / rel).write_text(text, encoding="utf-8")
    return root


def _brain(capsys, *argv: str) -> tuple[int, str]:
    parser = argparse.ArgumentParser()
    build_brain_parser(parser.add_subparsers())
    args = parser.parse_args(["brain", *argv])
    code = args.func(args)
    captured = capsys.readouterr()
    return code, captured.out + captured.err


def _person(*keys) -> str:
    registry = IdentityRegistry()
    try:
        return registry.resolve("person", list(keys)) if len(keys) > 1 else registry.person_of(keys[0])
    finally:
        registry.close()


def _name_person(page: Path, person: str) -> None:
    page.write_text(page.read_text(encoding="utf-8").replace("title: Mina\n", f"title: Mina\nperson_id: {person}\n"),
                    encoding="utf-8")


def _provider(home: Path, session_id: str):
    from plugins.memory.lina import LinaMemoryProvider
    provider = LinaMemoryProvider()
    provider.initialize(session_id, hermes_home=str(home), platform="cli")
    return provider


def _session_card(home: Path, session_id: str) -> str:
    provider = _provider(home, session_id)
    try:
        return provider.session_card_block()
    finally:
        provider.shutdown()


@pytest.fixture
def wiki(tmp_path):
    return make_wiki(tmp_path / "wiki")


def test_the_card_is_the_page_and_a_changed_page_is_the_next_sessions_card(capsys, tmp_path, wiki):
    _name_person(wiki / "people/mina.md", _person(OWNER_HANDLE))
    assert _brain(capsys, "import", str(wiki))[0] == 0

    first = _session_card(tmp_path / "home", "session-1")
    assert first.startswith("## Person card: Mina")
    assert "Prefers short reports." in first

    page = wiki / "people/mina.md"
    page.write_text(page.read_text(encoding="utf-8").replace("Prefers short reports.", "Prefers voice notes."),
                    encoding="utf-8")
    assert _brain(capsys, "reindex", str(wiki))[0] == 0

    second = _session_card(tmp_path / "home", "session-2")
    assert "Prefers voice notes." in second
    assert "Prefers short reports." not in second


class FakeConclusions:
    """Honcho's SDK as far as listing conclusions goes; any write is recorded so the test can see there was none."""

    def __init__(self, items: dict[str, list]) -> None:
        self.items = items
        self.writes: list[str] = []

    def peer(self, observer: str):
        fake = self

        class View:
            def __init__(self, target: str) -> None:
                self.target = target

            def list(self, page: int = 1, size: int = 50, **_kw):
                found = fake.items.get(observer, []) if self.target == "alice" else []
                return SimpleNamespace(items=found[(page - 1) * size:page * size])

            def create(self, *_a, **_kw):
                fake.writes.append("create")

            def delete(self, *_a, **_kw):
                fake.writes.append("delete")
        return SimpleNamespace(id=observer, conclusions_of=View)


def _conclusion(cid: str, text: str, day: int):
    return SimpleNamespace(id=cid, content=text, created_at=datetime(2026, 3, day, 9, tzinfo=UTC),
                           session_id="conversation", source_message_ids=[f"msg:{cid}"], source_ids=[])


def test_honcho_conclusions_land_on_the_page_once_with_grade_and_source(capsys, monkeypatch, tmp_path, wiki,
                                                                         memory_store_sql):
    home = tmp_path / "home"
    home.mkdir()
    (home / "honcho.json").write_text(json.dumps({"apiKey": "test-key", "enabled": True, "workspace": WORKSPACE,
                                                  "peerName": "alice", "aiPeer": "lina-bot"}), encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    fake = FakeConclusions({"alice": [_conclusion("c-1", "Likes early\nmeetings.", 1)],
                            "lina-bot": [_conclusion("c-2", "Works from the studio on Fridays.", 2),
                                         _conclusion("c-1", "Likes early\nmeetings.", 1),
                                         _conclusion("c-3", "The owner grows herbs.", 3)]})
    sources = {
        "msg:c-1": SimpleNamespace(metadata={"lina_speaker": "owner", "lina_grade": "owner"}),
        "msg:c-2": SimpleNamespace(metadata={"lina_speaker": "owner", "lina_grade": "owner"}),
        "msg:c-3": SimpleNamespace(metadata={"lina_speaker": "agent", "lina_grade": "agent"}),
    }
    fake.session = lambda ident: SimpleNamespace(get_message=sources.__getitem__)
    import plugins.memory.honcho.client as honcho_client
    monkeypatch.setattr(honcho_client, "get_honcho_client", lambda *a, **k: fake)
    person = _person(OWNER_HANDLE)
    assert _person(OWNER_HANDLE, ("honcho_peer", f"{WORKSPACE}:alice")) == person
    page = wiki / "people/mina.md"
    _name_person(page, person)
    assert _brain(capsys, "import", str(wiki))[0] == 0
    written = page.read_text(encoding="utf-8")

    code, out = _brain(capsys, "people", "import-honcho")
    assert code == 0, out
    assert "3 conclusions seen" not in out and "2 conclusions seen, 2 added" in out
    text = page.read_text(encoding="utf-8")
    assert "The owner grows herbs." not in text
    assert text.startswith(written)
    assert text[len(written):] == ("\n---\n\n## Timeline\n\n"
                                   "- 2026-03-01 | agent | honcho:c-1 | Likes early meetings.\n"
                                   "- 2026-03-02 | agent | honcho:c-2 | Works from the studio on Fridays.\n")
    assert memory_store_sql("SELECT grade, source_ref, text FROM brain.timeline WHERE page_slug = 'mina' "
                            "ORDER BY line") == [("agent", "honcho:c-1", "Likes early meetings."),
                                                 ("agent", "honcho:c-2", "Works from the studio on Fridays.")]

    code, out = _brain(capsys, "people", "import-honcho")
    assert code == 0 and "0 added" in out
    assert page.read_text(encoding="utf-8") == text
    assert fake.writes == []


def test_every_page_of_conclusions_is_read_and_each_id_once(monkeypatch):
    from plugins.memory.honcho import person_facts
    monkeypatch.setattr(person_facts, "PAGE_SIZE", 2)
    fake = FakeConclusions({"alice": [_conclusion(f"c-{day}", f"fact {day}", day) for day in range(1, 6)],
                            "lina-bot": [_conclusion("c-1", "fact 1", 1)]})

    found = person_facts.conclusions_about([fake.peer("alice"), fake.peer("lina-bot")], "alice")

    assert [item["id"] for item in found] == ["c-1", "c-2", "c-3", "c-4", "c-5"]
    assert found[0] == {"id": "c-1", "content": "fact 1", "created_at": "2026-03-01T09:00:00+00:00"}


def test_an_external_line_reaches_the_card_only_inside_the_data_block(capsys, wiki):
    from hermes_cli.brain import people
    person = _person(("gateway_user", "telegram:2002"))
    code, out = _brain(capsys, "people", "link", "haneul", "gateway_user:telegram:2002")
    assert code == 1 and "no brain page haneul" in out
    assert _brain(capsys, "import", str(wiki))[0] == 0
    assert _brain(capsys, "people", "link", "haneul", "gateway_user:telegram:2002")[0] == 0
    assert (wiki / "people/haneul.md").read_text(encoding="utf-8").startswith(f"---\nperson_id: {person}\n---\n# Haneul")

    import hermes_state_store
    with psycopg.connect(hermes_state_store.resolve_dsn()) as conn:
        text = people.card_for(conn, person)

    before, block = text.split("<external-data>")
    assert "Mixing engineer." in before and "newsletter" not in before
    assert "> heard about her from a newsletter" in block
    assert "[1] source: haneul | date: 2026-02-01 | from: external" in block


def test_one_person_has_one_page(capsys, wiki):
    person = _person(("gateway_user", "telegram:3003"))
    assert _brain(capsys, "import", str(wiki))[0] == 0
    assert _brain(capsys, "people", "link", "dana", person)[0] == 0
    code, out = _brain(capsys, "people", "link", "haneul", person)
    assert code == 1 and f"{person} already has page dana" in out
    code, out = _brain(capsys, "people", "card", "gateway_user:telegram:3003")
    assert code == 0 and out.startswith("## Person card: dana\n\n# Dana\n\nDesigner.")


def test_the_card_keeps_compiled_truth_and_the_newest_lines_that_fit():
    from hermes_cli.brain.people import PersonPage, TimelineLine, card
    lines = tuple(TimelineLine(date(2026, 1, day), "agent", f"honcho:c-{day}", f"fact number {day}")
                  for day in range(28, 0, -1))
    text = card(PersonPage("mina", "Mina", "Producer.", Path("people/mina.md"), lines), limit=400)

    assert len(text) <= 400
    assert "Producer." in text and "fact number 28" in text and "fact number 1\n" not in text
    assert text.index("fact number 27") < text.index("fact number 28")


def test_a_linked_page_replaces_honchos_person_sections_and_session_end_writes_its_conclusions(tmp_path, wiki,
                                                                                               capsys):
    _name_person(wiki / "people/mina.md", _person(OWNER_HANDLE))
    assert _brain(capsys, "import", str(wiki))[0] == 0
    calls = []

    class Honcho:
        def session_card_block(self, **kwargs):
            calls.append(kwargs)
            return "## Session Summary\nPlanned the release."

        def person_conclusions(self):
            return [{"id": "c-9", "content": "Answers in the morning.", "created_at": "2026-03-05T08:00:00+00:00"}]

        def on_session_end(self, messages):
            pass

        def shutdown(self):
            pass

    provider = _provider(tmp_path / "home", "session-1")
    provider._honcho, provider._honcho_on = Honcho(), True
    try:
        block = provider.session_card_block()
        assert calls == [{"person": False}]
        assert block.startswith("## Person card: Mina") and block.endswith("## Session Summary\nPlanned the release.")

        provider.on_session_end([])
    finally:
        provider.shutdown()
    assert (wiki / "people/mina.md").read_text(encoding="utf-8").endswith(
        "- 2026-03-05 | agent | honcho:c-9 | Answers in the morning.\n")
    assert "honcho:c-9" in _session_card(tmp_path / "home", "session-2")


def test_honchos_card_without_the_person_keeps_the_session_summary():
    from plugins.memory.honcho import HonchoMemoryProvider
    from plugins.memory.honcho.client import HonchoClientConfig

    class Manager:
        def get_prefetch_context(self, session, query, **kwargs):
            return {"summary": "Planned the release.", "representation": "likes gardening", "card": "Alice grows tomatoes",
                    "ai_card": "Speaks briefly"}

    provider = HonchoMemoryProvider(card_in_prompt=True)
    provider._config = HonchoClientConfig(timeout=1)
    provider._manager, provider._session_key, provider._session_initialized = Manager(), "session-a", True
    provider._recall_mode = provider._config.recall_mode

    block = provider.session_card_block(person=False)
    assert "Planned the release." in block and "Speaks briefly" in block
    assert "tomatoes" not in block and "gardening" not in block
    assert "tomatoes" in provider.session_card_block()
