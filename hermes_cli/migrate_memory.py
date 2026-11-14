"""Memory migration from retained copies into the running installation's new agent."""

from __future__ import annotations

import json
import re
import shutil
import subprocess
from argparse import Namespace
from dataclasses import asdict
from pathlib import Path

from hermes_cli.lina_gateway_key import provision, same_env_but_key
from hermes_cli.migrate_memory_agent import migrate_agent
from hermes_cli.migrate_memory_copy import _safe, snapshot
from hermes_cli.migrate_memory_external import migrate_external
from hermes_cli.migrate_memory_report import duplicate_report
from hermes_cli.migrate_memory_lcm import migrate_lcm
from hermes_state_store import StoreUnavailableError, _owner_connection


def _paths(args) -> tuple[Path, Path, Path]:
    from hermes_cli.store import store_dir

    running_home = store_dir().parent.absolute()
    source, home, work = (Path(value).expanduser().absolute()
                          for value in (args.from_, args.home or running_home, args.work_dir))
    if home != running_home:
        raise ValueError("--home must equal the running LINA root; run LINA from the destination home")
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


def _publish_config(copied: Path | None, destination: Path, *, lcm_store: bool) -> None:
    """The copy's config.yaml: the copied memory settings, LCM on the store when its history moved there, and a
    terminal scoped to the copy (lina#706). Without the scope its terminal starts in the host user's real home
    with that user's HOME and shell profile, where the original runtime's files are one command away."""
    from hermes_cli.config import atomic_config_write

    config = json.loads(copied.read_text()) if copied is not None else {}
    if lcm_store:
        config.update(context={"engine": "lcm-x"}, lcm={"store_backend": "postgres"})
    # home_mode "profile" points HOME at <copy>/home only when that directory exists.
    _safe(destination / "home").mkdir(exist_ok=True)
    config["terminal"] = {"cwd": str(destination), "home_mode": "profile", "auto_source_bashrc": False}
    atomic_config_write(_safe(destination / "config.yaml"), config)


def _publish_files(copied: Path, destination: Path, *, lcm_store: bool = False) -> list[str]:
    files = []
    for path in sorted(copied.rglob("*")):
        if not path.is_file() or path.name in {"state.db", "lcm.db", "work.db"}:
            continue
        relative = path.relative_to(copied)
        target = _safe(destination / relative)
        if relative == Path("config.yaml"):
            _publish_config(path, destination, lcm_store=lcm_store)
            files.append(str(relative))
            continue
        if target.exists():
            if target.is_symlink() or (target.read_bytes() != path.read_bytes() and not (
                    relative == Path(".env") and same_env_but_key(path, target))):
                raise ValueError(f"destination file differs: {relative}")
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(path, target)
        files.append(str(relative))
    return files


def _publish_honcho_config(selection: Path, destination: Path, profile: str, agent: str, store: Path,
                           owner_ids: list[str] | tuple[str, ...] = ()) -> None:
    """Publish new transport settings, never the source endpoint or credentials.

    The owner is recognised as in the source: its ``userPeerAliases`` and ``pinUserPeer`` carry over, and each
    *owner_ids* entry (the owner's runtime id on a platform the copy is reached by) becomes an alias to
    ``peerName``. Honcho imports the owner's memory files only for a session whose peer is ``peerName``."""
    from hermes_cli.store import DEFAULTS, read_env
    from plugins.memory.honcho.client import profile_host_key

    block = json.loads(selection.read_text(encoding="utf-8"))["hosts"][profile_host_key(profile)]
    port = int(read_env(store / "store.env").get("LINA_HONCHO_PORT", DEFAULTS["LINA_HONCHO_PORT"]))
    host = {"workspace": agent, "aiPeer": agent, "peerName": block["peerName"],
            "observationMode": block["observationMode"]}
    aliases = dict(block.get("userPeerAliases", {}))
    for owner_id in owner_ids:
        if aliases.setdefault(owner_id, block["peerName"]) != block["peerName"]:
            raise ValueError(f"--owner-id {owner_id} is aliased to another peer in the source Honcho configuration")
    if aliases:
        host["userPeerAliases"] = aliases
    if block.get("pinUserPeer"):
        host["pinUserPeer"] = True
    config = {"hosts": {profile_host_key(agent): {
        **host, "baseUrl": f"http://127.0.0.1:{port}", "enabled": True}}}
    target = _safe(destination / "honcho.json")
    if target.exists() and json.loads(target.read_text(encoding="utf-8")) != config:
        raise ValueError("destination Honcho configuration differs from this import")
    if not target.exists():
        target.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")


def _profile(args) -> dict:
    source, home, work = _paths(args)
    spool = Path(args.spool).expanduser() if args.spool else None
    if spool is not None and spool.is_dir():
        spool = spool / "events.jsonl"
    copied = snapshot(source, args.profile, work,
                      honcho_dump=Path(args.honcho_dump).expanduser() if args.honcho_dump else None,
                      spool=spool, work_db=Path(args.work_db).expanduser() if args.work_db else None)
    if copied.honcho_dump is not None and not (copied.root / "external/honcho-config.json").is_file():
        raise ValueError("Honcho dump requires the selected source profile's honcho.json host configuration")
    if args.owner_ids and copied.honcho_dump is None:
        raise ValueError("--owner-id names the owner's Honcho peer and requires --honcho-dump")
    if copied.work_db is not None:
        from hermes_cli.migrate_memory_work import register_profiles
        register_profiles(home, {args.profile: args.agent})
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
    lcm = migrate_lcm(copied.lcm_db, copied.state_db, destination, args.agent, work)
    honcho = {"status": "not_supplied", "counted": True, "ok": True, "reason": "no_archive_supplied"}
    if copied.honcho_dump is not None:
        from hermes_cli.migrate_memory_honcho import migrate_honcho
        honcho = migrate_honcho(copied.honcho_dump, args.profile, args.agent, work,
                               workspace=args.honcho_workspace,
                               selection_path=copied.root / "external/honcho-config.json",
                               honcho_python=Path(args.honcho_python) if args.honcho_python else None)
        honcho["counted"] = True
    if copied.spool is not None and spool is not None:
        reports.append(migrate_external(copied.spool, spool).as_dict())
    work_report = {"status": "not_supplied", "counted": True, "ok": True, "reason": "no_work_source_supplied"}
    if copied.work_db is not None:
        from hermes_cli.migrate_memory_work import migrate_work
        work_report = migrate_work(copied.work_db, home, args.profile, args.agent)
        work_report["counted"] = True
    files = _publish_files(copied.profile_dir, destination, lcm_store=copied.lcm_db is not None)
    provision(destination, args.agent, home)
    if "config.yaml" not in files:
        _publish_config(None, destination, lcm_store=copied.lcm_db is not None)
        files.append("config.yaml")
    if copied.honcho_dump is not None:
        _publish_honcho_config(copied.root / "external/honcho-config.json", destination,
                               args.profile, args.agent, home / "store", args.owner_ids)
        files.append("honcho.json")
    duplicates = duplicate_report(args.agent)
    steps = {"identity": {"counted": True, "ok": agent.ok}, "agent": {"counted": True, "ok": agent.ok},
             "lcm": {"counted": lcm["counted"], "ok": lcm["ok"]},
             "honcho": {"counted": honcho["counted"], "ok": honcho["ok"]},
             "external": {"counted": True, "ok": all(r["ok"] for r in reports[1:]),
                          "status": "imported" if copied.spool is not None else "not_supplied"},
             "embeddings": {"counted": True, "ok": True, "status": "accounted_deferred"},
             "work": {"counted": work_report["counted"], "ok": work_report["ok"]}}
    return {"format": 1, "round": 2, "profile": args.profile, "agent": args.agent,
            "destination_home": str(destination), "copies": [asdict(row) for row in copied.receipt],
            "sources": reports, "lcm": lcm, "honcho": honcho, "work": work_report,
            "files": files, "skipped_symlinks": list(copied.skipped_symlinks), "duplicates": duplicates,
            "complete": duplicates["ok"] and all(s["counted"] and s["ok"] for s in steps.values()),
            "steps": steps,
            "embeddings": {"status": "deferred", "counted": True, "ok": True, "command": "lina store reembed",
                           "reason": "No embedding endpoint is called; Honcho reports legacy vector disposition."},
            "ok": lcm["ok"] and all(report["ok"] for report in reports) and duplicates["ok"]
                  and honcho.get("ok", honcho.get("status") == "not_supplied")
                  and work_report.get("ok", work_report.get("status") == "not_supplied")}


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
            if args.work_db:
                from hermes_cli.migrate_memory_work import register_profiles
                _, home, _ = _paths(selections[0])
                register_profiles(home, {selected.profile: selected.agent for selected in selections})
            work = Path(args.work_dir).expanduser().absolute()
            reports = []
            for selected in selections:
                current = _profile(selected)
                reports.append(current)
                _safe(Path(selected.work_dir) / "migration-report.json").write_text(
                    json.dumps(current, ensure_ascii=False, indent=2, default=str) + "\n")
                if not current["ok"]:
                    break
            report = {"format": 1, "round": 2, "profiles": reports,
                      "complete": len(reports) == len(profiles) and all(item["complete"] for item in reports),
                      "ok": len(reports) == len(profiles) and all(item["ok"] for item in reports),
                      "lcm": {"counted": all(item["lcm"]["counted"] for item in reports)}}
        else:
            _, _, work = _paths(args)
            report = _profile(args)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError, StoreUnavailableError, psycopg.Error) as exc:
        # PostgreSQL errors may include source text or credentials: expose only their type.
        detail = str(exc) if isinstance(exc, ValueError) else type(exc).__name__
        report = {"format": 1, "round": 2, "ok": False, "error": detail,
                  "lcm": {"counted": False, "ok": False}}
    text = json.dumps(report, ensure_ascii=False, indent=2, default=str)
    if work is not None and work.is_dir():
        _safe(work / "migration-report.json").write_text(text + "\n")
    print(text)
    return 0 if report["ok"] else 1
