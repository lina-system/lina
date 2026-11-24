#!/usr/bin/env python3
"""Compare verified dashboard turn samples (standard library only)."""

import argparse
import csv
import json
import math
import statistics
import sys
from collections import defaultdict
from pathlib import Path
from typing import Final

MODES: Final = ("inproc", "python", "rust")
LATENCIES: Final = (
    "elapsed_ms", "model_excluded_ms", "fixture_service_ms", "submit_to_tool_start_ms",
    "tool_start_to_complete_ms", "tool_complete_to_message_complete_ms",
)


def load(paths: list[Path]) -> dict[str, dict[str, list[dict]]]:
    runs = defaultdict(dict)
    for path in paths:
        by_session = defaultdict(list)
        with path.open() as stream:
            for line_number, line in enumerate(stream, 1):
                row = json.loads(line)
                model = row["model_requests"]
                owner = row["host"] or row["parent"]
                events = row["events"]
                if (row["schema"] != 1 or row["impl"] not in MODES
                        or (row["host"] is None) != (row["impl"] == "inproc")
                        or [e["type"] for e in events] != ["tool.start", "tool.complete", "message.complete"]
                        or events[2]["payload"].get("status") != "complete"
                        or [request["phase"] for request in model] != [1, 2]
                        or any(request["owner"] != owner or request["tools"] != ["read_file"]
                               for request in model)
                        or any(event["payload"]["name"] != "read_file"
                               or event["payload"]["tool_id"] != model[1]["tool_result"]["tool_call_id"]
                               for event in events[:2])
                        or json.dumps(model[1]["tool_result"]["content"]).count(row["marker"]) != 1):
                    raise ValueError(f"{path}:{line_number}: invalid evidence")
                by_session[row["session_id"]].append(row)
        if not by_session:
            raise ValueError(f"{path}: no verified rows")
        for sid, rows in by_session.items():
            impl = rows[0]["impl"]
            if (sid in runs[impl] or any(row["impl"] != impl for row in rows)
                    or [row["turn"] for row in rows] != list(range(1, len(rows) + 1))
                    or len(rows) < 10):
                raise ValueError(f"{path}: invalid or incomplete 10-turn session {sid}")
            runs[impl][sid] = rows
    return runs


def percentile_pair(samples: list[float]) -> tuple[str, str]:
    if not samples:
        return "n/a", "n/a"
    ordered = sorted(samples)
    return f"{statistics.median(ordered):.3f}", f"{ordered[math.ceil(0.95 * len(ordered)) - 1]:.3f}"


def metrics(sessions: list[list[dict]]) -> list[tuple[str, str, str, list[float]]]:
    warm = [row for rows in sessions for row in rows[2:10]]
    values = [(name.removesuffix("_ms"), "ms", "turns_3_to_10",
               [row[name] for row in warm]) for name in LATENCIES]
    for role in ("parent", "host"):
        for component in ("user_ms", "sys_ms", "total_ms"):
            cpu = [row[f"{role}_cpu"] for row in warm if row[f"{role}_cpu"] is not None]
            samples = [item["user_ms"] + item["sys_ms"] if component == "total_ms"
                       else item[component] for item in cpu]
            values.append((f"{role}_cpu_{component.removesuffix('_ms')}", "ms", "turns_3_to_10", samples))
    for stage, index, window in (
        ("cold", 0, "before_turn_1"),
        ("cold_live", 0, "first_model_request"),
        ("after_first", 0, "idle_after_turn_1"),
        ("after_10", 9, "idle_after_turn_10"),
    ):
        points = [point for rows in sessions if (point := rows[index].get(f"{stage}_pss_kib"))]
        for role in ("parent", "host", "total"):
            samples = [
                point["parent"] + (point["host"] or 0) if role == "total" else point[role]
                for point in points if role == "total" or point[role] is not None
            ]
            values.append((f"{role}_pss", "KiB", window, samples))
    return values


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("files", nargs="+", type=Path)
    args = parser.parse_args()
    try:
        runs = load(args.files)
        table = []
        for mode in MODES:
            if mode not in runs:
                continue
            sessions = list(runs[mode].values())
            turns = sum(map(len, sessions))
            for metric, unit, window, samples in metrics(sessions):
                table.append((mode, metric, unit, window, len(sessions), turns,
                              len(samples), *percentile_pair(samples)))
    except (OSError, ValueError, KeyError, TypeError) as exc:
        parser.exit(1, f"Cannot compare measurements: {exc}\n")
    writer = csv.writer(sys.stdout, delimiter="\t", lineterminator="\n")
    writer.writerow(("impl", "metric", "unit", "window", "sessions", "verified_turns",
                     "samples", "p50", "p95"))
    writer.writerows(table)
    if missing := [mode for mode in MODES if mode not in runs]:
        print(f"Modes without supplied measurements: {', '.join(missing)}", file=sys.stderr)


if __name__ == "__main__":
    main()
