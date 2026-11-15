"""Owner-role word-pair and real CLI evaluation contracts (lina#381)."""

import re

import pytest

from hermes_cli.korean import eval as korean_eval, words
from hermes_cli.main import _build_cli_parser


def test_pairs_join_evaluation_and_default_checks_print_counts_only(memory_store_sql, monkeypatch, capsys):
    monkeypatch.setattr(korean_eval, "CHECKS", {})
    with words.owner_connection() as conn:
        words.add(conn, "슬롯", "자리", "calque",
                  pair=words.WordPair("슬롯을 비워 둡니다.", "자리를 비워 둡니다."))
        words.add(conn, "태스크", "할 일", "calque")
    with words.owner_connection() as conn:
        pairs = korean_eval.word_list_pairs(conn)
        assert [(p.avoid, p.use) for p in pairs] == [("슬롯", "자리")]
    parser, _ = _build_cli_parser()
    args = parser.parse_args(["eval", "korean"])
    with pytest.raises(SystemExit) as done:
        args.func(args)
    assert done.value.code == 0
    output = capsys.readouterr().out
    assert "(1 word-list pairs)" in output
    assert "check word-list" in output and "check particles" in output and "check calques" in output
    assert "1/1  100.0%" in output
    assert not re.search("[가-힣]", output)


def test_a_second_evaluation_reads_current_words_instead_of_a_previous_snapshot(memory_store_sql, monkeypatch):
    monkeypatch.setattr(korean_eval, "CHECKS", {})
    with words.owner_connection() as conn:
        words.add(conn, "슬롯", "자리", "calque")
        korean_eval.word_list_pairs(conn)
        assert korean_eval.CHECKS["word-list"]("슬롯") == [(0, 2)]
        words.retire(conn, "슬롯")
        korean_eval.word_list_pairs(conn)
        assert korean_eval.CHECKS["word-list"]("슬롯") == []
