"""Collect masked observed changes without models or senders."""
from __future__ import annotations
from datetime import datetime, timedelta
import difflib
import json
import math
from pathlib import Path
import re
import shlex
from hermes_cli.learned import storage, messages, text as display
from hermes_cli.work.common import mask
from contextlib import contextmanager
import sqlite3
import time
from hermes_cli.learned import config as config_module
from hermes_cli.learned.collector_skills import SkillCollection
from hermes_cli.learned.collector_memory import MemoryCollection
from hermes_cli.learned.collector_snapshots import SnapshotCollection
from hermes_cli.learned.collector_ledger import LedgerCollection

SOURCE_ERRORS = (OSError, ValueError, KeyError, TypeError, sqlite3.Error)


class CollectionError(Exception):
    def __init__(self, stage, error_class):
        self.stage, self.error_class = stage, error_class


@contextmanager
def collection_stage(name):
    try:
        yield
    except config_module.MissingProfiles:
        # Preserve the root fix's profile-name-only diagnostic.
        raise
    except SOURCE_ERRORS as error:
        raise CollectionError(name, type(error).__name__) from None


def mask_basis(records):
    """Mask message content while preserving structural lookup metadata."""
    return [dict(record, text=mask(record.get("text", ""))) for record in records]


class Collector(SkillCollection, MemoryCollection, SnapshotCollection, LedgerCollection):
    def __init__(self, config, state, dbs, now):
        self.config, self.state, self.dbs, self.now = config, state, dbs, now
        # Re-read the previous snapshot window for file attribution and late results.
        self.since = state.cursor.get("snapshot_at", self.cutoff(now).timestamp() - 86400)
        self.calls = dbs.calls(min(self.since, state.cursor.get("calls_since", self.since)))

    def cutoff(self, at):
        return datetime.fromtimestamp(at).replace(hour=self.config.hour, minute=self.config.minute,
                                                 second=0, microsecond=0)

    def day(self, at):
        local = datetime.fromtimestamp(at)
        if local >= self.cutoff(at):
            local += timedelta(days=1)
        return local.date().isoformat()

    def snapshot_day(self):
        cutoff = self.cutoff(self.now)
        if self.now < cutoff.timestamp():
            cutoff -= timedelta(days=1)
        return cutoff.date().isoformat()

    def basis(self, profile, session, at):
        return mask_basis(self.dbs.basis(profile, session, at))

    @staticmethod
    def ledger_event(event):
        return event.get("op") in ("add", "edit", "quote", "basis", "drop", "done", "confirm") and not event.get("_meta")

    def stream_ready(self, event, ledger=False):
        at = messages.timestamp(event.get("ts", event.get("at")) if ledger else event.get("ts"))
        if not at or not math.isfinite(at):
            if not ledger or self.ledger_event(event):
                raise ValueError("event has invalid time")
            # Ignored ledger metadata need not have timestamps, as before.
            return True
        return at <= self.now

    def item(self, key, day, kind, bot, target, before="", after="", actor="unknown", basis=None,
             source="seen", private=False, flags=None, ops=1, undo=None):
        flags = list(flags or [])
        if not basis:
            flags.append("no_basis")
        if private:
            flags.append("private")
            before, after, target, basis, undo = "", "", "private", [], {"how": "none"}
        row = dict(id="L" + day.replace("-", "") + "-" + storage.digest(key)[:16], day=day,
                   kind=kind, bot=bot, actor=actor, target=target, ops=ops,
                   summary="Private changes" if private else "Store change" if source == "seen" else "Bot report",
                   before=mask(before)[:600], after=mask(after)[:600], diff="", basis=mask_basis(basis or []),
                   flags=sorted(set(flags)), source=source, rank=0, undo=undo or {"how": "owner"}, status="new")
        if not private:
            diff = "".join(difflib.unified_diff(mask(before).splitlines(keepends=True),
                                               mask(after).splitlines(keepends=True), fromfile="before", tofile="after"))
            if diff:
                row["diff"] = "blobs/" + self.state.blob(diff)
        base = {"soul": 5, "rule": 5, "user": 4, "skill": 3 if actor == "background" else 2,
                "memory": 2, "conclusion": 1, "ledger": 1}[kind]
        row["rank"] = base + sum({"no_basis": 3, "time_mismatch": 3, "self_closed": 3,
                                  "deleted": 2, "regressed": 5}.get(flag, 0) for flag in row["flags"])
        return row

    def run(self):
        for name in ("skills", "conclusions", "ledger", "snapshots"):
            with collection_stage(name):
                getattr(self, name)()
        with collection_stage("commit"):
            self.state.commit()
            snapshot = {"at": self.state.cursor["snapshot_at"], "files": self.state.cursor["snapshot"]}
            storage.atomic(self.state.root / "snap" / (self.state.cursor["snapshot_day"] + ".json"),
                           json.dumps(snapshot, ensure_ascii=False))

def collect(now=None):
    now = time.time() if now is None else now
    with collection_stage("configuration"):
        config = config_module.Config()
        config.validate_profiles()
    with collection_stage("sources"):
        with storage.State(config.state) as state, messages.Messages(config.profiles, now) as dbs:
            with collection_stage("messages"):
                collector = Collector(config, state, dbs, now)
            collector.run()
