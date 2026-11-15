"""lina#377: `lina eval korean` imports a labeled Korean set into the store and scores every registered check on it.

The set below is synthetic (invented sentences). These run on the real Postgres from tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

import re

import psycopg
import pytest

from hermes_cli.korean import eval as korean_eval
from hermes_cli.main import _build_cli_parser
from hermes_cli.store_migrations import MIGRATIONS_DIR

HANGUL = re.compile(r"[가-힣]")

FLAGGED = [
    "작업 칸에 메모를 남겼어요.\t칸\tdefault\tsynthetic",
    "새 단어을 목록에 넣었어요.\t단어을",
    "회의를 슬롯에 잡아 두었어요.\t슬롯",
    "알림 칸이 비어 있어요.\t알림 칸",
]
CLEAN = [
    "점심은 김밥으로 먹었어요.\towner",
    "보고서를 내일까지 보낼게요.\treviewer\tgemini\tclaude",
    "책장 칸을 정리했어요.\towner",
]


def _naive_check(text: str) -> list[korean_eval.Span]:
    return [match.span() for match in re.finditer("칸|단어을", text)]


def _lina(*argv: str) -> int:
    parser, _ = _build_cli_parser()
    args = parser.parse_args(["eval", "korean", *argv])
    with pytest.raises(SystemExit) as done:
        args.func(args)
    return done.value.code


def _tsv(tmp_path, name: str, lines: list[str]) -> str:
    path = tmp_path / name
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
    return str(path)


def _clean_lines(n: int) -> list[str]:
    return [f"오늘 {i}번째 회의를 마쳤어요.\towner" for i in range(n)]


@pytest.fixture
def naive_check(monkeypatch):
    monkeypatch.setitem(korean_eval.CHECKS, "naive", _naive_check)


def test_every_line_is_imported_or_refused_with_a_reason_and_a_second_import_adds_nothing(
        tmp_path, capsys, memory_store_sql):
    flagged = _tsv(tmp_path, "flagged.tsv", [
        *FLAGGED,
        "문장만 있어요.",
        "문장에 없는 말이에요.\t바깥",
        "",
        "a\tb\tc\td\te",
    ])
    (tmp_path / "flagged.tsv").write_bytes((tmp_path / "flagged.tsv").read_bytes() + b"\xff\xfe\t\xff\n")
    clean = _tsv(tmp_path, "clean.tsv", [
        *CLEAN,
        "같은 계열이 봤어요.\treviewer\tclaude\tclaude",
        "계열이 없어요.\treviewer\tgemini",
        "누가 봤는지 몰라요.\tpassed",
        "주인이 봤어요.\towner\tgemini",
        "작업 칸에 메모를 남겼어요.\towner",
    ])

    assert _lina("import", "--flagged", flagged, "--clean", clean) == 1

    out = capsys.readouterr().out
    assert "9 flagged lines: 4 imported, 0 already in the set, 5 refused." in out
    assert "8 clean lines: 3 imported, 0 already in the set, 5 refused." in out
    for reason in ("line 5: no flagged phrase", "line 6: the flagged phrase is not in the sentence",
                   "line 7: empty line", "line 8: 5 columns, at most 4 expected", "line 9: not UTF-8",
                   "line 4: the reviewer's model family is the writer's",
                   "line 5: a reviewer mark needs the reviewer's model family and the writer's",
                   "line 6: marked_by must be owner or reviewer",
                   "line 7: an owner mark takes no reviewer model family",
                   "line 8: the same text is already labeled flagged"):
        assert reason in out
    assert not HANGUL.search(out)
    assert memory_store_sql("SELECT kind, count(*) FROM memory.korean_eval_texts GROUP BY kind ORDER BY kind") == [
        ("clean", 3), ("flagged", 4)]
    assert memory_store_sql("SELECT marked_by, marker_family, writer_family FROM memory.korean_eval_texts "
                            "WHERE marked_by = 'reviewer'") == [("reviewer", "gemini", "claude")]

    assert _lina("import", "--flagged", _tsv(tmp_path, "again.tsv", FLAGGED)) == 0
    assert "4 flagged lines: 0 imported, 4 already in the set, 0 refused." in capsys.readouterr().out


def test_the_report_gives_counts_and_wilson_intervals_per_check_and_no_text(tmp_path, capsys, naive_check):
    _lina("import", "--flagged", _tsv(tmp_path, "f.tsv", FLAGGED), "--clean", _tsv(tmp_path, "c.tsv", CLEAN))
    capsys.readouterr()

    assert _lina() == 0

    out = capsys.readouterr().out
    assert "Korean set: 4 flagged sentences with 4 flagged phrases (0 word-list pairs), 3 clean texts " \
           "(owner 2, reviewer 1)." in out
    assert "smoke only: 3 clean texts, fewer than 50" in out
    assert "check naive" in out
    # 3 of 4 sentences hit; the phrase 알림 칸 overlaps the span of 칸, 슬롯 is missed; 책장 칸 is a false alarm.
    assert re.search(r"flagged sentences caught\s+3/4  75\.0%  95% CI 30\.1-95\.4%", out)
    assert re.search(r"flagged phrases caught\s+3/4  75\.0%  95% CI 30\.1-95\.4%", out)
    assert re.search(r"false alarms on clean texts\s+1/3  33\.3%  95% CI 6\.1-79\.2%", out)
    assert not HANGUL.search(out)


def test_a_phrase_is_caught_only_when_a_span_overlaps_it(naive_check):
    labeled = korean_eval.LabeledSet([("칸 옆의 슬롯", "슬롯")], [], {}, 0)
    assert korean_eval.score(_naive_check, labeled) == korean_eval.Score(1, 1, 0, 1, 0, 0)


def test_the_gate_is_refused_with_fewer_than_50_clean_texts(tmp_path, capsys, naive_check):
    _lina("import", "--clean", _tsv(tmp_path, "c.tsv", _clean_lines(49)))
    capsys.readouterr()

    assert _lina("--gate") == 2

    assert "gate: refused: smoke only (49 clean texts, fewer than 50)" in capsys.readouterr().out


def test_the_gate_judges_the_upper_bound_of_false_alarms_from_50_clean_texts(tmp_path, capsys, naive_check):
    _lina("import", "--clean", _tsv(tmp_path, "c.tsv", _clean_lines(50)))
    capsys.readouterr()

    assert _lina("--gate") == 0
    out = capsys.readouterr().out
    assert "smoke only" not in out
    assert "gate: passed" in out

    # 0/50 has an upper bound of 7.1%: above a 5% limit.
    assert _lina("--gate", "--max-false-alarm", "0.05") == 1
    assert "gate: failed: false alarms on clean texts may exceed 5.0% (upper 95% bound): naive 7.1%." in \
           capsys.readouterr().out


def test_a_saved_baseline_holds_counts_only_and_a_later_run_prints_the_change(
        tmp_path, capsys, naive_check, memory_store_sql):
    _lina("import", "--flagged", _tsv(tmp_path, "f.tsv", FLAGGED[:2]), "--clean", _tsv(tmp_path, "c.tsv", CLEAN))
    assert _lina("--save-baseline", "before") == 0
    assert "Saved baseline before (counts only)." in capsys.readouterr().out
    saved = memory_store_sql("SELECT counts FROM memory.korean_eval_baselines WHERE name = 'before'")[0][0]
    assert saved["checks"] == {"naive": {"sentences_caught": 2, "sentences": 2, "phrases_caught": 2, "phrases": 2,
                                         "false_alarms": 1, "clean": 3}}
    assert not HANGUL.search(str(saved))

    _lina("import", "--flagged", _tsv(tmp_path, "more.tsv", FLAGGED[2:3]))
    capsys.readouterr()
    assert _lina() == 0

    out = capsys.readouterr().out
    assert "Compared with baseline before: then 2 flagged sentences, 2 flagged phrases, 3 clean texts." in out
    assert re.search(r"flagged sentences caught\s+2/3 .*\(baseline 2/2, -33\.3 pts\)", out)
    assert re.search(r"false alarms on clean texts\s+1/3 .*\(baseline 1/3, \+0\.0 pts\)", out)

    assert _lina("--save-baseline", "before") == 1
    assert "a baseline named before already exists" in capsys.readouterr().out
    assert _lina("--baseline", "missing") == 1


def test_an_agent_reads_only_its_own_texts_of_the_set(_memory_store, memory_store_sql):
    for text, agent in (("가 에이전트 문장", "a"), ("나 에이전트 문장", "b"), ("주인 문장", None)):
        memory_store_sql("INSERT INTO memory.korean_eval_texts (kind, text, text_sha256, marked_by, agent_id, source) "
                         "VALUES ('clean', %s, md5(%s), 'owner', %s, 'test')", (text, text, agent))
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        assert conn.execute("SELECT text FROM memory.korean_eval_texts").fetchall() == []
        conn.execute("SET lina.agent_id = 'a'")
        assert conn.execute("SELECT text FROM memory.korean_eval_texts").fetchall() == [("가 에이전트 문장",)]


def test_an_agent_cannot_write_the_set_or_read_the_baselines(_memory_store, memory_store_sql):
    memory_store_sql("INSERT INTO memory.korean_eval_baselines (name, counts) VALUES ('b', '{}'::jsonb)")
    with psycopg.connect(_memory_store.app_dsn(), autocommit=True) as conn:
        conn.execute("SET lina.agent_id = 'a'")
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("INSERT INTO memory.korean_eval_texts (kind, text, text_sha256, marked_by, agent_id, source) "
                         "VALUES ('clean', 'x', md5('x'), 'owner', 'a', 'test')")
        with pytest.raises(psycopg.errors.InsufficientPrivilege):
            conn.execute("SELECT name FROM memory.korean_eval_baselines")


def test_the_migration_runs_again_without_change(memory_store_sql):
    memory_store_sql((MIGRATIONS_DIR / "0017_eval_korean.sql").read_text(encoding="utf-8"))
    assert memory_store_sql("SELECT version FROM memory.schema_migrations WHERE version = '0017'") == [("0017",)]
