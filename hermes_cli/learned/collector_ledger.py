"""Read-only work record evidence verification."""
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

def norm(text):
    return "".join(c for c in text.casefold() if c.isalnum())

class LedgerCollection:
    def ledger(self):
        seen = self.state.cursor.setdefault("ledger_seen", [])
        users = None
        from hermes_cli.learned.upstream import work_events
        stream = work_events(self.config)
        for event, key in stream:
            if not self.stream_ready(event, ledger=True):
                break
            if key in seen:
                continue
            if not self.ledger_event(event):
                continue
            at = messages.timestamp(event.get("ts", event.get("at")))
            bot = str(event.get("writer") or event.get("actor") or event.get("by") or "unknown")
            basis = event.get("basis") or []
            if not basis and event.get("quote"):
                basis = [dict(text=event["quote"], at=event.get("said_at", 0), src=event.get("quote_source", event.get("said", "")))]
            checked, flags = [], []
            if basis and users is None:
                users = [(norm(record["text"]), record) for record in self.dbs.users()]
            for entry in basis:
                query = norm(str(entry.get("text", "")))
                matches = [record for text, record in users if query and query in text]
                claimed = messages.timestamp(entry.get("at"))
                match = min(matches, key=lambda r: abs(r["at"] - (claimed or at))) if matches else None
                status = "not_found" if match is None else "found"
                if claimed > at or (match and claimed and abs(claimed - match["at"]) > 600):
                    status = "time_mismatch"
                record = dict(match or {"kind": "owner", "profile": "", "session": "", "at": 0},
                              text=str(entry.get("text", "")), claimed_at=claimed, verification=status)
                checked.append(record)
                if status != "found":
                    flags.append("no_basis" if status == "not_found" else status)
            private = self.config.is_private("ledger", bot, str(event.get("id", "")), self.config.ledger)
            if event.get("op") == "done" and str(event.get("check", "")).startswith("evidence:"):
                owner = str(event.get("by", ""))
                start = self.cutoff(at)
                if at < start.timestamp():
                    start -= timedelta(days=1)
                for evidence in event.get("evidence", []):
                    ref = evidence.get("ref", "")
                    path = None
                    if ref.startswith("desk:"):
                        path = self.config.desk / ref[5:] if self.config.desk else None
                    elif ref.startswith("file:"):
                        path = Path(ref[5:]).expanduser().absolute()
                    if path:
                        private = private or self.config.is_private("rule", owner, path.name, path)
                        if self.writes(path, start.timestamp(), at, owner):
                            flags.append("self_closed")
            row = self.item(key, self.day(at), "ledger", bot, "ledger:" + str(event.get("id", "")),
                            after=str(event.get("title", event.get("done_evidence", ""))), actor="script",
                            basis=checked, private=private, flags=flags, undo={"how": "work"})
            row["op"] = event["op"]
            self.state.put(row)
            seen.append(key)
