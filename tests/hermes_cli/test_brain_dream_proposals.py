"""lina#444: `lina brain proposals accept|reject` records decisions, refuses external material without
--confirm-external, applies merges through the recorded merge and relation closes by validity, and the acceptance
view counts per phase."""

from __future__ import annotations

import json

import psycopg
import pytest

from tests.hermes_cli.test_brain_dream import FakeModel, _brain, _config, _hashes, _wiki


@pytest.fixture
def fake(monkeypatch):
    from hermes_cli.brain.dream import model
    found = FakeModel()
    monkeypatch.setattr(model, "aux_complete", found)
    return found


def _proposal(dsn: str, where: str) -> tuple[int, str]:
    with psycopg.connect(dsn) as conn:
        return conn.execute(f"SELECT proposal_id, status FROM brain.proposals WHERE {where}").fetchone()


def test_accept_and_reject_record_the_decision_and_apply_only_what_loses_no_text(_memory_store, tmp_path, capsys,
                                                                                 fake):
    root = _wiki(tmp_path / "wiki")
    _config(tmp_path)
    _brain(capsys, "import", str(root))
    _brain(capsys, "dream")
    files = _hashes(root)
    dsn = _memory_store.superuser_dsn()
    merge, _ = _proposal(dsn, "kind = 'merge'")
    drift, _ = _proposal(dsn, "phase = 'drift'")
    pattern, _ = _proposal(dsn, "phase = 'patterns'")

    _brain(capsys, "proposals", "accept", str(merge), code=1)
    assert _proposal(dsn, f"proposal_id = {merge}")[1] == "proposed"
    assert _brain(capsys, "proposals", "accept", str(merge), "--confirm-external") == f"Proposal #{merge}: applied.\n"
    assert _brain(capsys, "proposals", "accept", str(drift)) == f"Proposal #{drift}: accepted.\n"
    _brain(capsys, "proposals", "reject", str(pattern), "--by", "reviewer")
    _brain(capsys, "proposals", "reject", str(pattern), code=1)

    with psycopg.connect(dsn) as conn:
        assert conn.execute("SELECT from_slug, into_slug FROM brain.merges").fetchall() == [("star", "mina")]
        assert conn.execute("SELECT count(*) FROM brain.pages WHERE slug = 'star'").fetchone() == (0,)
        assert conn.execute("SELECT status, decided_by, decided_at IS NOT NULL FROM brain.proposals "
                            "WHERE proposal_id = %s", (pattern,)).fetchone() == ("rejected", "reviewer", True)
        assert conn.execute("SELECT compiled_truth LIKE '%%north studio%%' FROM brain.pages WHERE slug = 'mina'"
                            ).fetchone() == (True,)
    assert _hashes(root) == files
    rates = {row["phase"]: row for row in json.loads(_brain(capsys, "proposals", "rates", "--json"))["phases"]}
    assert (rates["consolidate"]["applied"], rates["drift"]["accepted"], rates["patterns"]["rejected"]) == (1, 1, 1)
    assert (float(rates["drift"]["acceptance_rate"]), float(rates["patterns"]["acceptance_rate"])) == (1.0, 0.0)
    assert rates["concepts"]["acceptance_rate"] is None
    listed = json.loads(_brain(capsys, "proposals", "list", "--status", "all", "--json"))["proposals"]
    assert {row["proposal_id"]: row["status"] for row in listed}[merge] == "applied"


def test_a_relation_close_sets_its_validity_and_keeps_the_edge(_memory_store, tmp_path, capsys, fake):
    root = _wiki(tmp_path / "wiki")
    _config(tmp_path)
    _brain(capsys, "import", str(root))
    dsn = _memory_store.superuser_dsn()
    with psycopg.connect(dsn) as conn:
        for target in ("label", "mixing"):
            conn.execute("INSERT INTO brain.edges (from_slug, to_slug, type, source_ref, valid_from) "
                         "VALUES ('mina', %s, 'works_at', 'people/mina.md', '2026-01-01')", (target,))

    _brain(capsys, "dream", "--phase", "contradictions")
    proposal, _ = _proposal(dsn, "kind = 'close_edge'")
    _brain(capsys, "proposals", "accept", str(proposal), "--confirm-external")
    _brain(capsys, "proposals", "accept", str(proposal), "--confirm-external", code=1)

    with psycopg.connect(dsn) as conn:
        assert conn.execute("SELECT to_slug, valid_until = current_date FROM brain.edges WHERE type = 'works_at' "
                            "ORDER BY to_slug").fetchall() == [("label", True), ("mixing", None)]
