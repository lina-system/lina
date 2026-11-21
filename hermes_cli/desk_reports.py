"""Register a shared Desk source and sync its report independently of other jobs."""

from __future__ import annotations

import os
import sys
from contextlib import closing
from pathlib import Path

import hermes_state_reports as reports
from hermes_cli.desk_report_files import ALLOWED, Refused, Source, identity, read_source

# The Desk is shared by profiles. A dedicated store scope keeps its registrations
# stable across callers without granting access to any profile's authored reports.
DESK_AGENT = "desk:reports"


def _save(conn, source: Source, report_id: str | None) -> tuple[str, str]:
    if report_id:
        conn.execute("UPDATE reports.report SET desk_state = 'active' WHERE report_id = %s", (report_id,))
        current = reports.get(conn, report_id)
    else:
        current = None
    title = reports.one_line(source.title)[:reports.TITLE_MAX]
    if current is None or (current.title, current.body) != (title, source.body):
        current = reports.save(conn, agent=DESK_AGENT, title=title, summary=title, body=source.body,
                               report_id=report_id)
        event = "updated" if report_id else "created"
    else:
        event = "unchanged"
    conn.execute("UPDATE reports.report SET desk_path = %s, desk_file_id = %s, desk_state = 'active' "
                 "WHERE report_id = %s", (source.path, source.identity, current.report_id))
    return current.report_id, event


def _lock(conn) -> None:
    # Serialize the absent-row case as well as moves versus re-registration.
    conn.execute("SELECT pg_advisory_xact_lock(hashtext('reports.desk'))")


def _register(conn, root, name, deny):
    with conn.transaction():
        _lock(conn)
        source = read_source(root, name, deny)
        row = conn.execute("SELECT report_id, desk_state FROM reports.report WHERE desk_path = %s",
                           (source.path,)).fetchone()
        if not row:
            candidates = conn.execute("SELECT report_id, desk_path FROM reports.report "
                                      "WHERE desk_file_id = %s AND desk_state = 'active'",
                                      (source.identity,)).fetchall()
            missing = [key for key, path in candidates if not os.path.lexists(root / path)]
            if len(missing) == 1:
                row = (missing[0], "moved")
        report_id, event = _save(conn, source, row[0] if row else None)
        return report_id, "updated" if row and row[1] != "active" else event


def _raise_walk_error(error: OSError) -> None:
    raise error


def _moved(root: Path, file_id: str) -> str | None:
    for folder in ALLOWED:
        base = root / folder
        try:
            base.lstat()
        except FileNotFoundError:
            continue
        for directory, _, files, dir_fd in os.fwalk(base, onerror=_raise_walk_error):
            for name in files:
                try:
                    info = os.stat(name, dir_fd=dir_fd, follow_symlinks=False)
                except FileNotFoundError:
                    continue
                if identity(info) == file_id:
                    return (Path(directory) / name).relative_to(root).as_posix()
    return None


def _sync_one(conn, root, deny, report_id):
    row = conn.execute("SELECT desk_path, desk_file_id, desk_state FROM reports.report WHERE report_id = %s",
                       (report_id,)).fetchone()
    path, file_id, previous = row
    if previous == "denied":
        return "unchanged"
    try:
        try:
            source = read_source(root, path, deny)
        except FileNotFoundError:
            moved = _moved(root, file_id) if previous == "active" else None
            if not moved:
                # A missing mount is a read failure for the pass, not a mass deletion.
                if not root.is_dir():
                    raise OSError("Desk unavailable")
                raise
            source = read_source(root, moved, deny)
            if source.identity != file_id:
                raise OSError("moved file changed while locating it")
            claimed = conn.execute("SELECT 1 FROM reports.report WHERE desk_path = %s AND report_id <> %s",
                                   (moved, report_id)).fetchone()
            if claimed:
                raise reports.ReportError("moved path already belongs to another report")
    except FileNotFoundError:
        state = "gone"
    except Refused:
        state = "denied"
    else:
        _, event = _save(conn, source, report_id)
        return "updated" if previous != "active" or source.path != path else event
    if state == previous:
        return "unchanged"
    conn.execute("UPDATE reports.report SET desk_state = %s WHERE report_id = %s", (state, report_id))
    return state


def _announce(report_id, event):
    from hermes_cli.lina_board_events import announce
    from hermes_cli.lina_deps import lina_root

    if event != "unchanged":
        announce(lina_root(), "reports", "created" if event == "created" else "updated", report_id)


def _sync(conn, root, deny) -> int:
    failed = False
    rows = conn.execute("SELECT report_id FROM reports.report WHERE desk_path IS NOT NULL "
                        "AND desk_state <> 'denied' ORDER BY desk_path").fetchall()
    for (report_id,) in rows:
        try:
            with conn.transaction():
                _lock(conn)
                event = _sync_one(conn, root, deny, report_id)
        except (OSError, UnicodeError, reports.ReportError) as exc:
            print(f"{report_id}: kept last copy ({exc})", file=sys.stderr)
            failed = True
            continue
        _announce(report_id, event)
        print(report_id, event)
    return int(failed)


def _deny_patterns(policy: Path) -> list[str]:
    # Carry forward the old tool's documented glob file. Local policy can add
    # restrictions without silently replacing the old ones during cutover.
    legacy = Path(os.environ.get("XDG_CONFIG_HOME") or Path.home() / ".config") / "sion-board" / "report-deny"
    lines: list[str] = []
    configured = False
    for file in (policy, legacy):
        try:
            lines.extend(file.read_text(encoding="utf-8").splitlines())
        except FileNotFoundError:
            continue
        configured = True
    if not configured:
        raise Refused(f"no report deny list configured; create {policy} (one Desk-relative glob per line)")
    return [line.strip() for line in lines if line.strip() and not line.lstrip().startswith("#")]


def cmd_desk_report(args) -> int:
    from hermes_cli.dashboard_auth.prefix import resolve_public_url
    from hermes_cli.desk import get_desk_path
    from hermes_constants import get_default_hermes_root
    from hermes_state_store import StoreUnavailableError

    root = get_desk_path()
    if root is None:
        print("no Desk configured or available", file=sys.stderr)
        return 2
    if not hasattr(os, "O_NOFOLLOW") or not hasattr(os, "fwalk"):
        print("this platform cannot open Desk reports without following links", file=sys.stderr)
        return 2
    try:
        deny = _deny_patterns(get_default_hermes_root() / "report-deny")
        with closing(reports.agent_connection(DESK_AGENT)) as conn:
            if args.desk_action == "sync-reports":
                return _sync(conn, root, deny)
            report_id, event = _register(conn, root, args.path, deny)
        _announce(report_id, event)
        print(reports.page_url(report_id, resolve_public_url()))
    except (Refused, reports.ReportError) as exc:
        print(f"refused: {exc}", file=sys.stderr)
        return 2
    except (OSError, UnicodeError, StoreUnavailableError) as exc:
        print(f"could not read or store the report: {exc}", file=sys.stderr)
        return 1
    return 0
