#!/usr/bin/env python3
"""Synthetic local probe and timing measurement for lina#381; never uses a model."""

from __future__ import annotations

import argparse
import os
import statistics
import sys
import time
from datetime import UTC, datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from hermes_cli.korean import checks, words

SEED = (
    ("슬롯", "자리"), ("태스크", "할 일"), ("워크플로", "작업 흐름"), ("컨텍스트", "맥락"),
    ("아웃풋", "결과"), ("인풋", "입력"), ("피드백", "의견"), ("인사이트", "발견"),
    ("리소스", "자원"), ("로드맵", "계획"), ("이슈", "문제"), ("패스", "길"),
)
TAIL = " 오늘 확인한 내용은 이 문장 뒤에 그대로 남겨 두었습니다."


def samples() -> list[tuple[str, str]]:
    result = []
    for avoid, use in SEED:
        result.append((avoid + "을 살폈습니다." + TAIL,
                       use + checks.particle_for(use, "을") + " 살폈습니다." + TAIL))
    result.extend([
        ("자리가 비었습니다." + TAIL, "자리가 비었습니다." + TAIL),
        ("자리을 살폈습니다." + TAIL, "자리를 살폈습니다." + TAIL),
        ("자리이 보입니다." + TAIL, "자리가 보입니다." + TAIL),
        ("자리은 비었습니다." + TAIL, "자리는 비었습니다." + TAIL),
        ("자리과 함께 봅니다." + TAIL, "자리와 함께 봅니다." + TAIL),
        ("자리으로 옮겼습니다." + TAIL, "자리로 옮겼습니다." + TAIL),
        ("길으로 걸었습니다." + TAIL, "길로 걸었습니다." + TAIL),
        ("오늘 결정을 만들었어요.", "오늘 결정을 만들었어요."),
    ])
    return result


def synthetic_entries() -> list[words.WordEntry]:
    return [words.WordEntry(index, avoid, use, "calque", None, datetime(2026, 1, 1, tzinfo=UTC))
            for index, (avoid, use) in enumerate(SEED, 1)]


def demonstrate(entries: list[words.WordEntry] | None) -> None:
    for index, (before, expected) in enumerate(samples(), 1):
        result = checks.check_reply(before, entries, names=())
        if result.text != expected or result.dropped:
            raise RuntimeError(f"synthetic case {index} failed ({result.reason})")
        print(f"{index:02d} before: {before}")
        print(f"   after: {result.text}")
        print(f"   edits={len(result.edits)} calques={len(result.calques)} dropped={result.dropped}")
    print("Synthetic replies: 20/20 passed.")


def benchmark() -> None:
    entries = synthetic_entries()
    text = ("슬롯을 살폈습니다. 확인한 내용을 천천히 읽었습니다. " * 50)[:1000]
    checks.check_reply(text, entries, names=())
    times = []
    for _ in range(1000):
        start = time.perf_counter_ns()
        checks.check_reply(text, entries, names=())
        times.append((time.perf_counter_ns() - start) / 1_000_000)
    print(f"Characters: {len(text)}; word entries: {len(entries)}; iterations: {len(times)}")
    print(f"Median added check time: {statistics.median(times):.6f} ms (preloaded entries; no store I/O)")


def store_probe() -> None:
    from hermes_cli.korean import eval as evaluation
    from hermes_cli.main import _build_cli_parser

    lina_home, hermes_home = os.environ.get("LINA_HOME"), os.environ.get("HERMES_HOME")
    if not lina_home or not hermes_home or Path(lina_home).resolve() != Path(hermes_home).resolve():
        raise SystemExit("Set LINA_HOME and HERMES_HOME to the same newly installed disposable home.")
    with words.owner_connection() as conn:
        if words.entries(conn, include_retired=True):
            raise SystemExit("Probe requires an empty disposable word list.")
        imported = words.import_seed(conn, (f"{avoid}\t{use}\tcalque" for avoid, use in SEED))
        if imported.added != 12 or imported.problems:
            raise RuntimeError("synthetic seed import failed")
        for (avoid, use), (before, after) in zip(SEED, samples(), strict=False):
            words.add(conn, avoid, use, "calque", pair=words.WordPair(before, after))
    print("Synthetic seed: 12 entries added; 12 stored pairs.")
    demonstrate(None)

    flagged = [(before, avoid) for (avoid, _), (before, _) in zip(SEED, samples(), strict=False)]
    flagged.extend((before, before.split()[0]) for before, after in samples()[13:19] if before != after)
    flagged.append(("오늘 결정을 만들었어요.", "결정을 만들"))
    scratch = Path(lina_home) / "korean-probe"
    scratch.mkdir()
    flagged_path, clean_path = scratch / "flagged.tsv", scratch / "clean.tsv"
    flagged_path.write_text("".join(f"{text}\t{phrase}\n" for text, phrase in flagged), encoding="utf-8")
    clean_path.write_text("".join(f"합성 예문 {index}: 오늘 내용을 읽었습니다.\towner\n"
                                  for index in range(50)), encoding="utf-8")
    with words.owner_connection() as conn:
        for kind, path in (("flagged", flagged_path), ("clean", clean_path)):
            result = evaluation.import_file(conn, kind, path, source="synthetic-korean-probe")
            if result.refused:
                raise RuntimeError(f"synthetic {kind} labels refused")
            print(f"Labels: {kind} imported={result.imported}")
    print("=== lina eval korean (counts only) ===")
    parser, _ = _build_cli_parser()
    args = parser.parse_args(["eval", "korean"])
    args.func(args)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--benchmark", action="store_true")
    mode.add_argument("--synthetic", action="store_true")
    mode.add_argument("--store", action="store_true")
    args = parser.parse_args()
    if args.store:
        store_probe()
    elif args.benchmark:
        benchmark()
    else:
        demonstrate(synthetic_entries())


if __name__ == "__main__":
    main()
