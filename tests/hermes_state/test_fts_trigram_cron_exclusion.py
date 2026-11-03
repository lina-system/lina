"""Cron transcripts remain searchable with word and CJK queries."""

import pytest

from hermes_state import SessionDB


@pytest.fixture
def db(tmp_path):
    with SessionDB(db_path=tmp_path / "state.db") as session_db:
        yield session_db


def test_cron_remains_searchable_via_standard_fts_and_explicit_cjk_fallback(
    db: SessionDB,
):
    db.create_session("cron", source="cron")
    db.append_message(
        "cron", role="assistant", content="quarterly archive 大别山项目 complete"
    )

    assert [row["session_id"] for row in db.search_messages("quarterly")] == [
        "cron"
    ]
    assert [
        row["session_id"]
        for row in db.search_messages("大别山项目", source_filter=["cron"])
    ] == ["cron"]
