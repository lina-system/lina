"""Revisions of the work settings (lina#256), kept beside the LINA root's ``config.yaml``:

```
<LINA root>/work-settings/revisions/r000001.yaml   # only the work-settings keys, one file per revision, never rewritten
<LINA root>/work-settings/audit.jsonl               # one line per revision: rev, at, by {kind, principal, device}, base,
                                                    # changes [{path, from, to}] (+ rollback_of)
<LINA root>/work-settings/lock                      # flock: one writer at a time, across processes
```

A change takes the lock, catches an outside edit first, validates the new settings, writes the changed keys to
``config.yaml`` in one atomic replacement (``write_keys``; comments and untouched keys stay), then links the next
revision file into place (``os.link`` fails on an existing name, so two writers can never make the same revision)
and appends its audit line. A refusal writes nothing.

An outside edit (``config.yaml`` edited by hand, or by anything but this layer) is caught on the next read: when the
keys there differ from the newest revision, they become a new revision with ``by.kind: outside`` before they are
served. Rollback writes the previous revision's content as a new revision (``by.kind: rollback``), so a second
rollback undoes the first. The revision before ``r1`` is the empty start ``r0``.
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import os
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

import hermes_yaml
from hermes_cli import lina_work_settings as ws

KINDS = ("cli", "app", "outside", "rollback")
EVENT_KIND = {"rollback": "rolled_back", "outside": "outside"}


class Stale(Exception):
    """The caller's base is not the newest revision."""

    def __init__(self, current: Revision):
        super().__init__(f"the settings changed since: now {current.id}")
        self.current = current


def rev_number(text: str) -> int | None:
    """``r42`` / ``r000042`` / ``42`` → 42."""
    raw = str(text).strip().strip('"')
    raw = raw[1:] if raw[:1] in "rR" else raw
    return int(raw) if raw.isdigit() else None


@dataclass(frozen=True)
class Revision:
    n: int
    doc: dict
    audit: dict | None = None

    @property
    def id(self) -> str:
        return f"r{self.n}"

    @property
    def etag(self) -> str:
        body = json.dumps(self.doc, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
        return f'"{self.id}-{hashlib.sha256(body.encode()).hexdigest()[:16]}"'


class Store:
    def __init__(self, base: Path | None = None):
        self.config = ws.config_path(base)
        self.dir = self.config.parent / "work-settings"
        self.revisions = self.dir / "revisions"
        self.audit = self.dir / "audit.jsonl"
        self.lockfile = self.dir / "lock"

    # ---------------------------------------------------------------- files
    @contextlib.contextmanager
    def lock(self):
        import fcntl  # the hub, like lina worker's slots, runs on a POSIX host
        self.revisions.mkdir(parents=True, exist_ok=True)
        with open(self.lockfile, "a", encoding="utf-8") as fh:
            fcntl.flock(fh, fcntl.LOCK_EX)
            try:
                yield
            finally:
                fcntl.flock(fh, fcntl.LOCK_UN)

    def _path(self, n: int) -> Path:
        return self.revisions / f"r{n:06d}.yaml"

    def numbers(self) -> list[int]:
        if not self.revisions.is_dir():
            return []
        return sorted(n for p in self.revisions.glob("r*.yaml") if (n := rev_number(p.stem)) is not None)

    def lines(self) -> list[dict]:
        if not self.audit.is_file():
            return []
        out = []
        for line in self.audit.read_text(encoding="utf-8-sig").splitlines():
            with contextlib.suppress(ValueError):
                out.append(json.loads(line))
        return out

    def revision(self, n: int) -> Revision:
        """Revision *n* (``r0`` = the empty start); ``KeyError`` when there is none."""
        if n == 0:
            return Revision(0, {})
        path = self._path(n)
        if not path.is_file():
            raise KeyError(f"r{n}")
        doc = hermes_yaml.safe_load(path.read_text(encoding="utf-8-sig")) or {}
        audit = next((line for line in self.lines() if line.get("rev") == f"r{n}"), None)
        return Revision(n, doc if isinstance(doc, dict) else {}, audit)

    def latest(self) -> Revision:
        numbers = self.numbers()
        return self.revision(numbers[-1]) if numbers else Revision(0, {})

    def _write(self, base: Revision, doc: dict, by: dict, changes: list[dict], **extra) -> Revision:
        """Revision ``base.n + 1`` with its audit line; called under the lock."""
        n = base.n + 1
        at = time.time()
        header = f"# r{n} by {by.get('kind')}:{by.get('principal')}@{by.get('device')} base r{base.n}\n"
        tmp = self.revisions / f".r{n:06d}.{os.getpid()}.tmp"
        tmp.write_text(header + hermes_yaml.safe_dump(doc, sort_keys=False), encoding="utf-8")
        try:
            os.link(tmp, self._path(n))  # fails when the name exists: never two writers on one revision
        finally:
            tmp.unlink(missing_ok=True)
        line = {"rev": f"r{n}", "at": at, "by": by, "base": f"r{base.n}", "changes": changes, **extra}
        with open(self.audit, "a", encoding="utf-8") as fh:
            fh.write(json.dumps(line, ensure_ascii=False) + "\n")
            fh.flush()
            os.fsync(fh.fileno())
        return Revision(n, doc, line)

    # ---------------------------------------------------------------- reads
    def _outside(self) -> Revision:
        """Under the lock: the newest revision, after recording an outside edit of ``config.yaml`` as its own."""
        latest = self.latest()
        found = ws.snapshot(ws.read_config(self.config))
        if found == latest.doc:
            return latest
        by = {"kind": "outside", "principal": "", "device": ""}
        return self._write(latest, found, by, ws.diff(latest.doc, found))

    def current(self) -> Revision:
        """The settings now. An outside edit becomes a revision first (that is the only write a read makes)."""
        latest = self.latest()
        if ws.snapshot(ws.read_config(self.config)) == latest.doc:
            return latest
        with self.lock():
            return self._outside()

    # ---------------------------------------------------------------- writes
    def _commit(self, base: Revision, new: dict, by: dict, **extra) -> tuple[Revision, list[dict]]:
        ws.check(new)
        changes = ws.diff(base.doc, new)
        if not changes:
            return base, []
        # Dotted sets/removals at or below a work-settings key only: nothing else in config.yaml is named, so nothing
        # else can be replaced or dropped (no deletion by omission; a removal is always an explicit key).
        raw = ws.read_config(self.config)
        refused = ws.remnants(base.doc, new, raw)
        if refused:
            raise ws.SettingsError(refused)
        write_keys(self.config, ws.settings_updates(base.doc, new, raw))
        stored = ws.snapshot(ws.read_config(self.config))  # what landed is the revision, so the next read agrees
        rev = self._write(base, stored, by, ws.diff(base.doc, stored), **extra)
        return rev, rev.audit["changes"]

    def apply(self, edit: Callable[[dict], dict], by: dict, expect: str | None = None) -> tuple[Revision, list[dict]]:
        """One change: *edit* gets a copy of the newest settings and returns the new ones. *expect* (an ETag or a
        revision id) refuses with ``Stale`` when it is not the newest."""
        with self.lock():
            base = self._outside()
            if expect is not None and expect not in (base.etag, base.id):
                raise Stale(base)
            return self._commit(base, edit(ws.plain(base.doc)), by)

    def rollback(self, by: dict, expect: str | None = None) -> tuple[Revision, list[dict]]:
        """Write the revision before the newest as a new revision."""
        with self.lock():
            base = self._outside()
            if expect is not None and expect not in (base.etag, base.id):
                raise Stale(base)
            if base.n == 0:
                raise ws.SettingsError([{"path": "", "message": "no revision to roll back yet"}])
            previous = self.revision(base.n - 1)
            return self._commit(base, previous.doc, {**by, "kind": "rollback"}, rollback_of=base.id)

    def history(self, limit: int = 20) -> list[dict]:
        return list(reversed(self.lines()))[:max(0, limit)]


def write_keys(path: Path, updates: dict) -> None:
    """One settings change as ONE write through the config writer seam. On the file backend: the guarded read
    (``require_readable_config_before_write``: an unreadable file or a non-mapping root is refused, never read as
    ``{}``), every set/unset applied in memory, then one ``atomic_config_replace`` (one temp file + atomic replace,
    comments kept). ``write_config_keys`` there would replace the file once per key, so a ``lina worker`` command
    reading in between, or an interrupted write, would see half a change. Elsewhere ``write_config_keys``, already
    one CAS patch."""
    from hermes_cli.config_backend import supports_file_tooling, write_config_keys
    if not supports_file_tooling():
        write_config_keys(path, updates)
        return
    from hermes_cli.config import atomic_config_replace
    current = ws.read_config(path)
    try:
        atomic_config_replace(path, ws.apply_updates(current, updates))  # removals here are deliberate
    except RuntimeError as exc:  # the seam's own fail-closed refusal (the file changed under us into one it can't read)
        raise ws.SettingsError([{"path": "config.yaml", "message": ws.UNREADABLE}]) from exc


def cli_by() -> dict:
    """The CLI on the hub: the OS user, device ``local``."""
    import getpass
    try:
        user = getpass.getuser()
    except (KeyError, OSError):
        user = "unknown"
    return {"kind": "cli", "principal": user, "device": "local"}
