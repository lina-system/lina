"""lina#25: ``lina memory cache-report`` computes the lina#17 baseline's numbers from MAGPIE usage rows."""

from __future__ import annotations

import argparse
import json

import pytest

from hermes_cli import cache_measure
from hermes_cli.subcommands.memory import build_memory_parser

SECRETS = ("conv-SECRET-7f3a", "conv-SECRET-b210", "req-SECRET-91", "https://secret.example/v1", "alice@example.com")


def _row(ts: str, conversation: str, total=None, read=0, write=0, write_1h=0, output=10, **extra) -> dict:
    row = {"timestamp": ts, "conversationId": conversation, "cacheReadInputTokens": read,
           "cacheCreationInputTokens": write, "cacheCreation1hInputTokens": write_1h, "outputTokens": output,
           "requestId": SECRETS[2], "endpoint": SECRETS[3], "accountName": SECRETS[4], **extra}
    if total is not None:
        row["inputTokens"] = total
    return row


# The baseline day's totals (lina#17): 448,630,335 input, 440,141,533 read, 8,486,386 written; two unreported rows.
KNOWN = [
    _row("2026-11-01T00:10:00+09:00", SECRETS[0], 400_000_000, 399_000_000, 1_000_000, 600_000),
    _row("2026-11-01T01:00:00+09:00", SECRETS[0]),
    _row("2026-11-01T02:00:00+09:00", SECRETS[1], 48_000_000, 41_141_533, 6_856_051, 0),
    _row("2026-11-01T03:00:00+09:00", SECRETS[1], None, inputTokens=None),
    _row("2026-11-01T04:00:00+09:00", SECRETS[1], 630_335, 0, 630_335, 630_335),
    _row("2026-11-02T00:00:00+09:00", SECRETS[1], 1_000, 0, 0, 0),
]


def _write(path, rows) -> str:
    path.write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    return str(path)


def _cli(*argv: str) -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    build_memory_parser(parser.add_subparsers(dest="command"), cmd_memory=lambda args: None)
    return parser.parse_args(["memory", "cache-report", *argv])


def test_known_usage_reproduces_the_baseline_totals_and_rate(tmp_path, capsys):
    usage = _write(tmp_path / "usage.jsonl", KNOWN)
    assert cache_measure.run(_cli("--usage", usage, "--from", "2026-11-01T00:00:00+09:00",
                                  "--to", "2026-11-02T00:00:00+09:00", "--format", "tsv")) == 0
    fields = {line.split("\t")[1]: line.split("\t")[2] for line in capsys.readouterr().out.splitlines()
              if line.startswith("all\t")}

    assert fields["metered"] == "3" and fields["unreported"] == "2" and fields["requests"] == "5"
    assert (fields["input_total"], fields["cache_read"]) == ("448630335", "440141533")
    assert (fields["cache_write_5m"], fields["cache_write_1h"]) == ("7256051", "1230335")
    assert fields["hit_rate_weighted"] == "98.10784%"
    assert fields["hit_rate_mean"] == f"{(99.75 + 100 * 41_141_533 / 48_000_000 + 0) / 3:.5f}%"
    assert fields["hit_rate_median"] == f"{100 * 41_141_533 / 48_000_000:.5f}%"


def test_conversation_filter_keeps_only_the_named_conversation(tmp_path):
    with open(_write(tmp_path / "usage.jsonl", KNOWN), encoding="utf-8") as handle:
        rows = cache_measure.load_usage(handle, conversations=[SECRETS[0]])
    rate = cache_measure.rates(rows)
    assert (rate.metered, rate.unreported, rate.input_total) == (1, 1, 400_000_000)


@pytest.mark.parametrize("fmt", ["md", "tsv"])
def test_no_id_or_account_from_the_input_reaches_the_output(tmp_path, capsys, fmt):
    usage = _write(tmp_path / "usage.jsonl", KNOWN)
    assert cache_measure.run(_cli("--usage", usage, "--baseline", "98.10784", "--format", fmt)) == 0
    out = capsys.readouterr().out
    assert "conversation 1" in out and "conversation 2" in out
    assert not [s for s in SECRETS if s in out]


def test_retreat_follows_each_conversation_and_counts_block_sized_breaks():
    rows = [
        _row("2026-11-01T00:00:00+00:00", "a", 1_000, 0),
        _row("2026-11-01T00:01:00+00:00", "b", 9_000, 0),
        _row("2026-11-01T00:02:00+00:00", "a", 2_000, 0),
        _row("2026-11-01T00:03:00+00:00", "a", None),
        _row("2026-11-01T00:04:00+00:00", "a", 5_000, 1_024),
        _row("2026-11-01T00:05:00+00:00", "b", 9_500, 8_192),
        _row("2026-11-01T00:06:00+00:00", "a", 6_000, 0),
    ]
    back = cache_measure.retreat(cache_measure.load_usage(json.dumps(r) for r in rows))
    assert (back.tokens, back.breaks, back.largest) == (1_000 + 976 + 808 + 5_000, 1, 5_000)


def test_comparison_is_within_noise_inside_two_standard_errors_and_directional_outside():
    rows = [_row(f"2026-11-01T00:{m:02d}:00+00:00", "a", 10_000, 9_000 + 100 * (m % 3)) for m in range(30)]
    rate = cache_measure.rates(cache_measure.load_usage(json.dumps(r) for r in rows))
    assert rate.stderr and rate.stderr > 0
    assert cache_measure.compare(rate, rate.weighted + rate.stderr)[1] == "within noise"
    assert cache_measure.compare(rate, rate.weighted - 3 * rate.stderr)[1] == "above"
    assert cache_measure.compare(rate, rate.weighted + 3 * rate.stderr)[1] == "below"


def test_a_time_without_offset_is_refused(tmp_path, capsys):
    usage = _write(tmp_path / "usage.jsonl", KNOWN)
    assert cache_measure.run(_cli("--usage", usage, "--from", "2026-11-01T00:00:00")) == 2
    assert "no UTC offset" in capsys.readouterr().err
