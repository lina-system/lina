"""Consume the retired Desk registry as one all-or-nothing snapshot."""

from __future__ import annotations

import hashlib
import json
import re
import sys
from pathlib import Path

from hermes_cli.desk_report_files import (
    MARKDOWN_LIMIT, Refused, Source, bounded_read, check_path, current_identity, open_regular,
)
from hermes_cli.desk_report_images import registry_images

SLUG = re.compile(r"[a-z0-9][a-z0-9_-]*")


def _bytes(root: Path, name: str, limit: int | None = None) -> bytes:
    # Callers supply only a constant or a validated single-component slug.
    with open_regular(root, Path(name)) as fd:
        assert fd is not None
        if limit is not None:
            return bounded_read(fd, limit)
        with open(fd, "rb", closefd=False) as stream:
            return stream.read()


def _import_one(conn, registry, desk, deny, slug, row):
    from hermes_cli.desk_reports import DESK_AGENT, _save
    from hermes_state_reports import TITLE_MAX, one_line

    rel = row.get("rel")
    if not isinstance(rel, str) or not isinstance(row.get("gone"), bool):
        raise Refused("missing relative path or withdrawal state")
    path = check_path(rel, deny).as_posix()
    existing = conn.execute("SELECT desk_path, report_id FROM reports.desk_alias WHERE slug = %s",
                            (slug,)).fetchone()
    if existing and (existing[0] != path or (existing[1] is None) != row["gone"]):
        raise Refused("slug already mapped to a different registration")
    report_id = None
    given_title = None
    if not row["gone"]:
        file_id = current_identity(desk, rel, deny)
        data = _bytes(registry, f"{slug}.md", MARKDOWN_LIMIT)
        if hashlib.sha256(data).hexdigest()[:16] != row.get("sha"):
            raise Refused("document checksum mismatch")
        body = data.decode("utf-8").replace("\r\n", "\n").replace("\r", "\n").strip()
        title = row.get("title")
        if not isinstance(title, str) or not one_line(title) or len(one_line(title)) > TITLE_MAX or not body:
            raise Refused("invalid published title or empty document")
        if "\0" in title or "\0" in body:
            raise Refused("document contains a null character")
        images, diagnostics = registry_images(registry, row, body, deny, desk_root=desk)
        for reason in diagnostics:
            print(f"{slug}: {reason}", file=sys.stderr)
        if existing:
            # A retry still validates the entire backup. It must never undo a
            # subsequent Desk edit, move or withdrawal.
            return
        target = conn.execute("SELECT report_id FROM reports.report WHERE desk_path = %s", (path,)).fetchone()
        source = Source(path, file_id or f"snapshot:{slug}", body, title, images=images)
        report_id, _ = _save(conn, source, target[0] if target else None)
        if row.get("title_from") == "given":
            given_title = title
    if not existing:
        conn.execute("INSERT INTO reports.desk_alias (slug, desk_path, agent_id, report_id, given_title) "
                     "VALUES (%s, %s, %s, %s, %s)", (slug, path, DESK_AGENT, report_id, given_title))


def import_registry(conn, registry: Path, desk: Path, deny: list[str]) -> int:
    from psycopg import IntegrityError

    from hermes_cli.desk_reports import _lock

    index = json.loads(_bytes(registry, "index.json"))
    if not isinstance(index, dict) or not isinstance(index.get("reports"), list):
        raise Refused("expected a reports list in index.json")
    seen: set[str] = set()
    failed = 0
    with conn.transaction():
        _lock(conn)
        for number, row in enumerate(index["reports"], 1):
            slug = row.get("slug") if isinstance(row, dict) else None
            if not isinstance(slug, str) or not SLUG.fullmatch(slug):
                print(f"entry {number}: invalid slug", file=sys.stderr)
                failed += 1
                continue
            try:
                if slug in seen:
                    raise Refused("duplicate slug")
                seen.add(slug)
                with conn.transaction():
                    _import_one(conn, registry, desk, deny, slug, row)
            except (ValueError, OSError, IntegrityError) as exc:
                reason = "conflicting registration" if isinstance(exc, IntegrityError) else str(exc)
                print(f"{slug}: {reason}", file=sys.stderr)
                failed += 1
        if failed:
            raise Refused(f"{failed} registry entries failed; no changes applied")
    withdrawn = sum(row["gone"] for row in index["reports"])
    print(f"validated {len(seen)} registrations: {len(seen) - withdrawn} active, {withdrawn} withdrawn")
    return 0
