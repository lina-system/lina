"""Prompt-cache hit rate from OCX usage rows, by the method of the lina#17 baseline (lina#25).

``lina memory cache-report --usage <usage.jsonl>`` reads only the usage fields of each row (``inputTokens``,
``cacheReadInputTokens``, ``cacheCreationInputTokens``, ``cacheCreation1hInputTokens``, ``outputTokens``,
``conversationId``, ``timestamp``); no transcript and no message text. ``inputTokens`` is OCX's total input, cache
reads and creations included, so the token-weighted hit rate is ``sum(cache_read) / sum(input_total)`` over the
metered rows. A row without ``inputTokens`` (a cancelled request) is unreported: it is counted apart and never as a
miss or as zero. ``cacheCreationInputTokens`` is every cache write; ``cacheCreation1hInputTokens`` is the 1h-tier
part of it and the rest is the 5m tier.

A cache retreat is ``max(0, previous_input_total - current_cache_read)`` between consecutive metered requests of
one conversation. With ``--fingerprints`` (the opt-in capture of ``agent/prefix_fingerprint.py``) the exact common
prefix of consecutive requests of a session is measured in whole message blocks.

The output names conversations by number in order of first appearance; ids, request ids, endpoints and accounts in
the input never reach it.
"""

from __future__ import annotations

import json
import math
import statistics
import sys
from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from datetime import datetime
from pathlib import Path

# A retreat of at least one cache block (the provider caches in 1024-token steps) counts as a break; a smaller one
# is the unaligned tail of the previous request.
BREAK_TOKENS = 1024
# A rate differs from a baseline only when the gap exceeds this many standard errors of the rate.
NOISE_SE = 2.0


@dataclass(frozen=True)
class UsageRow:
    timestamp: datetime
    conversation: str
    input_total: int | None
    cache_read: int
    cache_write: int
    cache_write_1h: int
    output: int


@dataclass(frozen=True)
class Rates:
    requests: int
    metered: int
    unreported: int
    input_total: int
    cache_read: int
    write_5m: int
    write_1h: int
    output: int
    weighted: float | None
    mean: float | None
    median: float | None
    stderr: float | None


@dataclass(frozen=True)
class Retreat:
    tokens: int
    breaks: int
    largest: int


@dataclass(frozen=True)
class PrefixKeep:
    requests: int
    kept_whole: int
    broken: int
    kept_bytes: int
    previous_bytes: int


def _parse_time(value: str) -> datetime:
    moment = datetime.fromisoformat(value)
    if moment.tzinfo is None:
        raise ValueError(f"time {value!r} has no UTC offset")
    return moment


def _count(row: dict, key: str) -> int:
    value = row.get(key)
    return int(value) if value is not None else 0


def load_usage(lines: Iterable[str], *, start: datetime | None = None, end: datetime | None = None,
               conversations: Sequence[str] = ()) -> list[UsageRow]:
    """Usage rows with ``start <= timestamp < end`` and, when given, one of ``conversations``; file order kept."""
    wanted = set(conversations)
    rows = []
    for number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            raw = json.loads(line)
            moment = _parse_time(raw["timestamp"])
            total = raw.get("inputTokens")
            row = UsageRow(moment, str(raw.get("conversationId") or ""), int(total) if total is not None else None,
                           _count(raw, "cacheReadInputTokens"), _count(raw, "cacheCreationInputTokens"),
                           _count(raw, "cacheCreation1hInputTokens"), _count(raw, "outputTokens"))
        except (ValueError, KeyError, TypeError) as exc:
            raise ValueError(f"usage line {number}: {exc}") from None
        if (start and moment < start) or (end and moment >= end) or (wanted and row.conversation not in wanted):
            continue
        rows.append(row)
    return rows


def rates(rows: Sequence[UsageRow]) -> Rates:
    metered = [r for r in rows if r.input_total is not None]
    total = sum(r.input_total for r in metered)
    read = sum(r.cache_read for r in metered)
    write = sum(r.cache_write for r in metered)
    write_1h = sum(r.cache_write_1h for r in metered)
    per_request = [100 * r.cache_read / r.input_total for r in metered if r.input_total]
    weighted = 100 * read / total if total else None
    stderr = None
    if weighted is not None and len(metered) > 1:
        # Linearized standard error of a ratio estimator: each request's residual from the pooled rate.
        ratio = read / total
        residuals = sum((r.cache_read - ratio * r.input_total) ** 2 for r in metered)
        stderr = 100 * math.sqrt(len(metered) / (len(metered) - 1) * residuals) / total
    return Rates(len(rows), len(metered), len(rows) - len(metered), total, read, write - write_1h, write_1h,
                 sum(r.output for r in metered), weighted,
                 statistics.fmean(per_request) if per_request else None,
                 statistics.median(per_request) if per_request else None, stderr)


def compare(rate: Rates, baseline: float) -> tuple[float | None, str]:
    """(difference in points, ``above`` / ``below`` / ``within noise`` / ``no data``) against a baseline percent."""
    if rate.weighted is None:
        return None, "no data"
    difference = rate.weighted - baseline
    if rate.stderr is not None and abs(difference) <= NOISE_SE * rate.stderr:
        return difference, "within noise"
    return difference, "above" if difference > 0 else "below"


def labels(rows: Sequence[UsageRow]) -> dict[str, str]:
    """Conversation id -> ``conversation N``, numbered in order of first appearance."""
    order: dict[str, str] = {}
    for row in rows:
        order.setdefault(row.conversation, f"conversation {len(order) + 1}")
    return order


def retreat(rows: Sequence[UsageRow]) -> Retreat:
    previous: dict[str, int] = {}
    steps = []
    for row in sorted(rows, key=lambda r: r.timestamp):
        if row.input_total is None:
            continue
        if row.conversation in previous:
            steps.append(max(0, previous[row.conversation] - row.cache_read))
        previous[row.conversation] = row.input_total
    return Retreat(sum(steps), sum(1 for s in steps if s >= BREAK_TOKENS), max(steps, default=0))


def load_fingerprints(lines: Iterable[str]) -> list[dict]:
    rows = []
    for number, line in enumerate(lines, 1):
        if not line.strip():
            continue
        try:
            raw = json.loads(line)
            rows.append({"timestamp": _parse_time(raw["ts"]), "session": str(raw["session"]),
                         "blocks": [(str(h), int(n)) for h, n in raw["blocks"]]})
        except (ValueError, KeyError, TypeError) as exc:
            raise ValueError(f"fingerprint line {number}: {exc}") from None
    return rows


def prefix_keep(fingerprints: Sequence[dict]) -> PrefixKeep:
    """Whole leading blocks each request shares with the session's previous request."""
    previous: dict[str, list[tuple[str, int]]] = {}
    requests = whole = kept_bytes = previous_bytes = 0
    for row in sorted(fingerprints, key=lambda r: r["timestamp"]):
        before = previous.get(row["session"])
        previous[row["session"]] = row["blocks"]
        if before is None:
            continue
        common = 0
        for a, b in zip(before, row["blocks"]):
            if a != b:
                break
            common += a[1]
        size = sum(n for _, n in before)
        requests += 1
        whole += common == size
        kept_bytes += common
        previous_bytes += size
    return PrefixKeep(requests, whole, requests - whole, kept_bytes, previous_bytes)


def _pct(value: float | None) -> str:
    return "n/a" if value is None else f"{value:.5f}%"


def _rate_fields(rate: Rates) -> list[tuple[str, str]]:
    return [
        ("requests", str(rate.requests)), ("metered", str(rate.metered)), ("unreported", str(rate.unreported)),
        ("input_total", str(rate.input_total)), ("cache_read", str(rate.cache_read)),
        ("cache_write_5m", str(rate.write_5m)), ("cache_write_1h", str(rate.write_1h)), ("output", str(rate.output)),
        ("hit_rate_weighted", _pct(rate.weighted)), ("hit_rate_mean", _pct(rate.mean)),
        ("hit_rate_median", _pct(rate.median)),
        ("hit_rate_stderr", "n/a" if rate.stderr is None else f"{rate.stderr:.5f} pt"),
    ]


def report(rows: Sequence[UsageRow], *, baselines: Sequence[float] = (), fingerprints: Sequence[dict] | None = None,
           fmt: str = "md") -> str:
    overall = rates(rows)
    back = retreat(rows)
    named = labels(rows)
    groups = [(label, rates([r for r in rows if r.conversation == cid])) for cid, label in named.items()]
    fields = _rate_fields(overall) + [
        ("retreat_tokens", str(back.tokens)), (f"retreat_breaks_ge_{BREAK_TOKENS}", str(back.breaks)),
        ("retreat_largest", str(back.largest))]
    verdicts = [(base, *compare(overall, base)) for base in baselines]
    keep = prefix_keep(fingerprints) if fingerprints is not None else None
    if keep is not None:
        fields += [("prefix_requests", str(keep.requests)), ("prefix_kept_whole", str(keep.kept_whole)),
                   ("prefix_broken", str(keep.broken)),
                   ("prefix_kept_bytes", _pct(100 * keep.kept_bytes / keep.previous_bytes if keep.previous_bytes else None))]
    if fmt == "tsv":
        out = ["scope\tfield\tvalue", *(f"all\t{k}\t{v}" for k, v in fields)]
        for label, rate in groups:
            out += [f"{label}\t{k}\t{v}" for k, v in _rate_fields(rate)]
        for base, diff, verdict in verdicts:
            out.append(f"all\tbaseline_{base:.5f}\t{'n/a' if diff is None else f'{diff:+.5f} pt'} {verdict}")
        return "\n".join(out) + "\n"
    out = ["# Cache report", "", "| field | value |", "| --- | --- |", *(f"| {k} | {v} |" for k, v in fields)]
    if verdicts:
        out += ["", f"Noise rule: a gap within {NOISE_SE:g} standard errors of the token-weighted rate "
                "(linearized ratio estimator over metered requests) is within noise.", "",
                "| baseline | difference | verdict |", "| --- | --- | --- |"]
        out += [f"| {base:.5f}% | {'n/a' if diff is None else f'{diff:+.5f} pt'} | {verdict} |"
                for base, diff, verdict in verdicts]
    out += ["", "| conversation | metered | unreported | input_total | hit_rate_weighted |", "| --- | --- | --- | --- | --- |"]
    out += [f"| {label} | {rate.metered} | {rate.unreported} | {rate.input_total} | {_pct(rate.weighted)} |"
            for label, rate in groups]
    return "\n".join(out) + "\n"


def run(args) -> int:
    try:
        start = _parse_time(args.start) if args.start else None
        end = _parse_time(args.end) if args.end else None
        with open(args.usage, encoding="utf-8-sig") as handle:
            rows = load_usage(handle, start=start, end=end, conversations=args.conversation or ())
        fingerprints = None
        if args.fingerprints:
            with open(Path(args.fingerprints), encoding="utf-8-sig") as handle:
                fingerprints = load_fingerprints(handle)
    except (OSError, ValueError) as exc:
        print(f"cache-report: {exc}", file=sys.stderr)
        return 2
    sys.stdout.write(report(rows, baselines=args.baseline or (), fingerprints=fingerprints, fmt=args.format))
    return 0
