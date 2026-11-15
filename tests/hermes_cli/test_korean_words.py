"""The owner's shared word list (lina#378): who reads and writes it, how it grows, and where its pairs may go."""

import argparse
import json
import logging
import unicodedata
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path

import psycopg
import pytest

import hermes_state_store
from hermes_cli.korean import words
from hermes_cli.subcommands.words import build_words_parser

SEED = Path(__file__).resolve().parents[1] / "fixtures" / "korean_words_seed.tsv"
MIGRATION = Path(__file__).resolve().parents[2] / "services" / "store" / "migrations" / "0016_word_list.sql"
FLAGGED = "Synthetic flagged sentence with 슬롯 in it."
FIXED = "Synthetic fixed sentence with 자리 in it."


def invoke(*argv):
    parser = argparse.ArgumentParser()
    build_words_parser(parser.add_subparsers())
    args = parser.parse_args(["words", *argv])
    out, err = StringIO(), StringIO()
    code = 0
    with redirect_stdout(out), redirect_stderr(err):
        try:
            args.func(args)
        except SystemExit as error:
            code = error.code
    return code, out.getvalue(), err.getvalue()


def as_agent(agent: str):
    conn = psycopg.connect(hermes_state_store.resolve_dsn(), autocommit=True)
    conn.execute("SELECT set_config('lina.agent_id', %s, false)", (agent,))
    return conn


def owner_correction(memory_store_sql) -> str:
    uid = "agent:alpha:fixture-correction"
    memory_store_sql("INSERT INTO memory.messages (uid, source, agent_id, role, content, content_sha256) "
                     "VALUES (%s, 'agent', 'alpha', 'user', %s, 'fixture')", (uid, "Synthetic correction"))
    return uid


def test_every_agent_reads_the_list_and_no_agent_writes_it(memory_store_sql):
    assert invoke("add", "--avoid", "슬롯", "--use", "자리", "--reason", "calque",
                  "--flagged", FLAGGED, "--fixed", FIXED)[0] == 0
    with as_agent("beta") as conn:
        assert conn.execute("SELECT avoid_word, use_word FROM memory.word_list").fetchall() == [("슬롯", "자리")]
        for statement in ("INSERT INTO memory.word_list (avoid_word, use_word, reason) VALUES ('a', 'b', 'other')",
                          "UPDATE memory.word_list SET use_word = 'b'",
                          "DELETE FROM memory.word_list",
                          "SELECT flagged FROM memory.word_pairs"):
            with pytest.raises(psycopg.errors.InsufficientPrivilege):
                conn.execute(statement)
    assert memory_store_sql("SELECT use_word, retired_at FROM memory.word_list") == [("자리", None)]


def test_the_refiner_role_writes_entries_and_pairs(memory_store_sql):
    with words.owner_connection() as conn:
        conn.execute("SET ROLE lina_refiner")
        entry, created = words.add(conn, "태스크", "할 일", "calque", pair=words.WordPair(FLAGGED, FIXED))
        assert created
        assert words.pairs(conn) == {entry.entry_id: [words.WordPair(FLAGGED, FIXED)]}
    with words.owner_connection() as conn, pytest.raises(psycopg.errors.InsufficientPrivilege):
        conn.execute("SET ROLE lina_refiner")
        conn.execute("DELETE FROM memory.word_list")
    assert memory_store_sql("SELECT avoid_word FROM memory.word_list") == [("태스크",)]


def test_a_correction_twice_leaves_one_entry_with_its_source(memory_store_sql):
    uid = owner_correction(memory_store_sql)
    command = ("add", "--avoid", "슬롯", "--use", "자리", "--reason", "calque", "--source", uid,
               "--flagged", FLAGGED, "--fixed", FIXED)
    first, second = invoke(*command), invoke(*command)
    assert first[0] == 0 and first[1].startswith("added #1 슬롯 -> 자리")
    assert second[0] == 0 and second[1].startswith("already in the list #1")
    assert memory_store_sql("SELECT avoid_word, use_word, reason, source_uid FROM memory.word_list") == [
        ("슬롯", "자리", "calque", uid)]
    assert memory_store_sql("SELECT count(*) FROM memory.word_pairs") == [(1,)]
    decomposed = unicodedata.normalize("NFD", " 슬롯 ")
    assert invoke("add", "--avoid", decomposed, "--use", "자리", "--reason", "calque")[1].startswith("already")


def test_retiring_an_entry_removes_it_from_the_next_read(memory_store_sql):
    invoke("add", "--avoid", "슬롯", "--use", "자리", "--reason", "calque")
    invoke("add", "--avoid", "리뷰", "--use", "검토", "--reason", "calque")
    assert [entry.avoid for entry in words.word_list_entries()] == ["슬롯", "리뷰"]
    code, output, _ = invoke("retire", "--avoid", "슬롯", "--source", "agent:alpha:fixture-retire")
    assert code == 0 and output.startswith("retired #1")
    assert [entry.avoid for entry in words.word_list_entries()] == ["리뷰"]
    assert invoke("retire", "--avoid", "슬롯")[0] == 1
    assert memory_store_sql("SELECT retired_source_uid FROM memory.word_list WHERE entry_id = 1") == [
        ("agent:alpha:fixture-retire",)]


def test_pairs_stay_out_of_lists_exports_and_logs_unless_asked(memory_store_sql, caplog):
    caplog.set_level(logging.DEBUG)
    invoke("add", "--avoid", "슬롯", "--use", "자리", "--reason", "calque", "--flagged", FLAGGED, "--fixed", FIXED)
    invoke("retire", "--avoid", "슬롯")
    shown = [invoke("list")[1], invoke("list", "--all")[1], invoke("list", "--all", "--json")[1],
             caplog.text, repr(words.WordPair(FLAGGED, FIXED))]
    assert "슬롯 -> 자리" in shown[1]
    for text in shown:
        assert FLAGGED not in text and FIXED not in text
    exported = json.loads(invoke("list", "--all", "--json", "--with-pairs")[1])
    assert exported[0]["pairs"] == [{"flagged": FLAGGED, "fixed": FIXED, "source": None}]
    assert FLAGGED in invoke("list", "--all", "--with-pairs")[1]


def test_the_seed_imports_with_no_line_dropped(memory_store_sql):
    code, output, error = invoke("import", str(SEED))
    assert (code, output.strip(), error) == (0, "12 lines: 12 added, 0 already in the list, 0 not imported", "")
    assert invoke("import", str(SEED))[1].strip() == "12 lines: 0 added, 12 already in the list, 0 not imported"
    assert memory_store_sql("SELECT count(*) FROM memory.word_list") == [(12,)]


def test_the_seed_reports_every_line_it_cannot_import(tmp_path, memory_store_sql):
    seed = tmp_path / "seed.tsv"
    seed.write_text("슬롯\t자리\tcalque\n\n리뷰\t검토\n피드백\t의견\tstyle\n셋업\t셋업\tcalque\n디폴트\t기본값\tother\n",
                    encoding="utf-8")
    code, output, error = invoke("import", str(seed))
    assert code == 1
    assert output.strip() == "6 lines: 2 added, 0 already in the list, 4 not imported"
    assert [line.split(":")[0] for line in error.splitlines()] == ["line 2", "line 3", "line 4", "line 5"]
    assert memory_store_sql("SELECT avoid_word FROM memory.word_list ORDER BY entry_id") == [("슬롯",), ("디폴트",)]


def test_the_read_api_keeps_entries_whose_words_appear_in_the_terms(memory_store_sql):
    invoke("import", str(SEED))
    assert len(words.word_list_entries()) == 12
    assert [e.avoid for e in words.word_list_entries(["슬롯을 비워", unicodedata.normalize("NFD", "맥락")])] == [
        "슬롯", "컨텍스트"]
    assert words.word_list_entries(["unrelated"]) == []
    assert words.word_list_entries([]) == []


def test_the_migration_runs_again_on_a_store_with_rows(memory_store_sql):
    invoke("add", "--avoid", "슬롯", "--use", "자리", "--reason", "calque", "--flagged", FLAGGED, "--fixed", FIXED)
    memory_store_sql(MIGRATION.read_text(encoding="utf-8"))
    assert memory_store_sql("SELECT count(*) FROM memory.word_list") == [(1,)]
    assert memory_store_sql("SELECT count(*) FROM memory.word_pairs") == [(1,)]
    with as_agent("beta") as conn, pytest.raises(psycopg.errors.InsufficientPrivilege):
        conn.execute("DELETE FROM memory.word_list")
