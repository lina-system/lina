"""Round-one memory migration from retained copies into a separate agent home."""

from __future__ import annotations

import json
import re
import shutil
import subprocess
from argparse import Namespace
from dataclasses import asdict
from pathlib import Path

from hermes_cli.migrate_memory_agent import migrate_agent
from hermes_cli.migrate_memory_copy import _safe, snapshot
from hermes_cli.migrate_memory_external import migrate_external
from hermes_cli.migrate_memory_report import duplicate_report, lcm_waiting_for_issue_16
from hermes_state_store import StoreUnavailableError, _owner_connection


def _paths(args) -> tuple[Path, Path, Path]:
    source, home, work = (Path(value).expanduser().absolute()
                          for value in (args.from_, args.home, args.work_dir))
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", args.agent) or args.agent == args.profile:
        raise ValueError("--as must name a distinct new agent using letters, digits, underscores or hyphens")
    for path in (source, home, work):
        if ".." in path.parts or any(part.is_symlink() for part in (path, *path.parents)):
            raise ValueError("migration paths cannot traverse symlinks or parent components")
    for other in (source, work):
        if home.is_relative_to(other) or other.is_relative_to(home):
            raise ValueError("destination home must be separate from source and work directories")
    if source.is_relative_to(work) or work.is_relative_to(source):
        raise ValueError("source and work directories must be separate")
    return source, home, work


def _publish_files(copied: Path, destination: Path) -> list[str]:
    files = []
    for path in sorted(copied.rglob("*")):
        if not path.is_file() or path.name in {"state.db", "lcm.db", "work.db"}:
            continue
        relative = path.relative_to(copied)
        target = _safe(destination / relative)
        if target.exists():
            if target.is_symlink() or target.read_bytes() != path.read_bytes():
                raise ValueError(f"destination file differs: {relative}")
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        files.append(str(relative))
    return files


def _profile(args) -> dict:
    source, home, work = _paths(args)
    spool = Path(args.spool).expanduser() if args.spool else None
    if spool is not None and spool.is_dir():
        spool = spool / "events.jsonl"
    copied = snapshot(source, args.profile, work,
                      honcho_dump=Path(args.honcho_dump).expanduser() if args.honcho_dump else None,
                      spool=spool, work_db=Path(args.work_db).expanduser() if args.work_db else None)
    destination = _safe(home / "profiles" / args.agent)
    receipt_path = _safe(work / "destination.json")
    receipt = {"home": str(home), "agent": args.agent, "profile": args.profile,
               "snapshot": str(copied.root)}
    if receipt_path.exists():
        if json.loads(receipt_path.read_text()) != receipt:
            raise ValueError("work directory belongs to another destination")
    else:
        if destination.exists():
            raise ValueError("destination agent home already exists without this migration receipt")
        with _owner_connection() as conn:
            if conn.execute("SELECT 1 FROM memory.messages WHERE agent_id=%s LIMIT 1", (args.agent,)).fetchone():
                raise ValueError("destination agent already owns messages")
            if conn.execute("SELECT 1 FROM memory.identity_keys WHERE kind='agent' AND source='profile' "
                            "AND key=%s", (args.agent,)).fetchone():
                raise ValueError("destination agent is already registered")
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")
    agent = migrate_agent(copied.state_db, destination / "state.db", args.agent)
    reports = [agent.as_dict()]
    lcm = lcm_waiting_for_issue_16()
    honcho = {"status": "not_supplied", "counted": False}
    if copied.honcho_dump is not None:
        from hermes_cli.migrate_memory_honcho import migrate_honcho
        honcho = migrate_honcho(copied.honcho_dump, args.profile, args.agent, work,
                               workspace=args.honcho_workspace,
                               honcho_python=Path(args.honcho_python) if args.honcho_python else None)
    if copied.spool is not None and spool is not None:
        reports.append(migrate_external(copied.spool, spool).as_dict())
    work_report = {"status": "not_supplied", "counted": False}
    if copied.work_db is not None:
        from hermes_cli.migrate_memory_work import migrate_work
        work_report = migrate_work(copied.work_db, home, args.profile, args.agent)
    files = _publish_files(copied.profile_dir, destination)
    duplicates = duplicate_report(args.agent)
    gaps = [work_report["gap"]] if work_report.get("status") == "gap" else []
    return {"format": 1, "round": 1, "profile": args.profile, "agent": args.agent,
            "destination_home": str(destination), "copies": [asdict(row) for row in copied.receipt],
            "sources": reports, "lcm": lcm, "honcho": honcho, "work": work_report,
            "files": files, "duplicates": duplicates, "gaps": gaps,
            "complete": False,
            "embeddings": {"status": "deferred", "command": "lina store reembed",
                           "reason": "No embedding endpoint is called; Honcho reports legacy vector disposition."},
            "ok": all(report["ok"] for report in reports) and duplicates["ok"]
                  and honcho.get("ok", honcho.get("status") == "not_supplied")}


def run(args) -> int:
    """Print and persist the same report; input failures never claim success."""
    import psycopg

    work = None
    try:
        if args.all_profiles:
            source = Path(args.from_).expanduser()
            profiles = (["default"] if (source / "state.db").is_file() else [])
            named = source / "profiles"
            if named.is_dir():
                profiles.extend(path.name for path in sorted(named.iterdir()) if (path / "state.db").is_file())
            if not profiles:
                raise ValueError("no legacy profiles found")
            selections = [Namespace(**{**vars(args), "profile": profile, "agent": f"{args.agent}-{profile}",
                                       "work_dir": str(Path(args.work_dir) / profile), "all_profiles": False})
                          for profile in profiles]
            for selected in selections:
                _paths(selected)
            work = Path(args.work_dir).expanduser().absolute()
            reports = []
            for selected in selections:
                current = _profile(selected)
                reports.append(current)
                _safe(Path(selected.work_dir) / "migration-report.json").write_text(
                    json.dumps(current, ensure_ascii=False, indent=2, default=str) + "\n")
                if not current["ok"]:
                    break
            report = {"format": 1, "round": 1, "profiles": reports, "complete": False,
                      "ok": len(reports) == len(profiles) and all(item["ok"] for item in reports),
                      "lcm": lcm_waiting_for_issue_16()}
        else:
            _, _, work = _paths(args)
            report = _profile(args)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, StoreUnavailableError, psycopg.Error) as exc:
        # PostgreSQL errors may include source text or credentials: expose only their type.
        detail = str(exc) if isinstance(exc, ValueError) else type(exc).__name__
        report = {"format": 1, "round": 1, "ok": False, "error": detail,
                  "lcm": lcm_waiting_for_issue_16()}
    text = json.dumps(report, ensure_ascii=False, indent=2, default=str)
    if work is not None and work.is_dir():
        _safe(work / "migration-report.json").write_text(text + "\n")
    print(text)
    return 0 if report["ok"] else 1
