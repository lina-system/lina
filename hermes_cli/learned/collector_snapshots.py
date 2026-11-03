"""Daily file snapshots and conservative writer attribution."""
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

class SnapshotCollection:
    def writes(self, path, since, until, bot=None):
        matches = []
        for call in self.calls:
            if call["name"] not in ("write_file", "patch", "patch_file", "edit_file", "terminal"):
                continue
            if not since <= call["at"] <= until or (bot is not None and call["profile"] != bot):
                continue
            if not messages.confirmed(call["result"]):
                continue
            args = call["args"]
            names = [str(args.get(k, "")) for k in ("path", "file_path", "filename", "file")]
            command = str(args.get("command", "")) if call["name"] == "terminal" else ""
            home = self.config.profiles[call["profile"]]
            variants = {str(path), str(path.resolve())}
            for base, prefix in ((Path.home(), "~/"), (home, "")):
                try:
                    variants.add(prefix + str(path.relative_to(base)))
                except ValueError:
                    pass
            # Full path matching avoids attributing a write to a different same-named file.
            if any(name in variants for name in names if name) or self.terminal_writes(command, variants):
                matches.append(call)
        return matches

    @staticmethod
    def terminal_writes(command, variants):
        # Conservative recognition: redirection destinations and explicit writers.
        # Arbitrary scripts cannot reliably be attributed just from a path mention.
        try:
            lexer = shlex.shlex(command, posix=True, punctuation_chars=";&|<>")
            lexer.whitespace_split = True
            tokens = list(lexer)
        except ValueError:
            return False
        for index, token in enumerate(tokens):
            if token in (">", ">>", ">|") and index + 1 < len(tokens) and tokens[index + 1] in variants:
                return True
        if not tokens:
            return False
        executable = Path(tokens[0]).name
        if executable == "tee":
            return any(token in variants for token in tokens[1:])
        if executable in ("cp", "mv", "install"):
            return tokens[-1] in variants
        if executable in ("sed", "perl") and any(token.startswith("-i") for token in tokens[1:]):
            return any(token in variants for token in tokens[1:])
        return False

    def capture(self, path, kind, bot):
        raw = path.read_text(encoding="utf-8-sig", errors="replace")
        private = self.config.is_private("memory" if kind == "user" else kind, bot, path.name, path)
        # Paths/IDs are local structure, not prose: content masks can corrupt
        # long paths and make later attribution or private-source lookup fail.
        entry = dict(kind=kind, bot=bot, target="private" if private else str(path),
                     private=private, fingerprint=storage.digest(raw), blob="", entries=[])
        if not private:
            if kind in ("memory", "user"):
                entry["usage"] = self.memory_usage(bot, path.name, len(raw))
                for text in raw.split("\n§\n"):
                    text = text.strip()
                    if text:
                        hidden = self.config.is_private("memory", bot, path.name, path, text)
                        entry["entries"].append({"private": True, "hash": storage.digest(text)} if hidden else
                                                {"private": False, "text": mask(text)})
            else:
                entry["blob"] = self.state.blob(mask(raw))
        return entry

    def snapshots(self):
        day = self.snapshot_day()
        if self.state.cursor.get("snapshot_day") == day:
            return
        previous = self.state.cursor.get("snapshot", {})
        current, paths = {}, {}
        for bot, home in self.config.profiles.items():
            for filename in ("MEMORY.md", "USER.md"):
                path = home / "memories" / filename
                if path.exists():
                    key = storage.digest(str(path))
                    current[key] = self.capture(path, "user" if filename == "USER.md" else "memory", bot)
                    paths[key] = path
        for path in self.config.rule_files():
            key = storage.digest(str(path))
            if key in current:
                continue
            bot = next((bot for bot, home in self.config.profiles.items() if path == home / "SOUL.md"), "unknown")
            current[key] = self.capture(path, "soul" if path.name == "SOUL.md" else "rule", bot)
            paths[key] = path
        if "snapshot" in self.state.cursor:
            for key in sorted(previous.keys() | current.keys()):
                old, new = previous.get(key), current.get(key)
                if old and new and old["fingerprint"] == new["fingerprint"]:
                    continue
                info = new or old
                empty = dict(info, entries=[], blob="", fingerprint="")
                old, new = old or empty, new or empty
                path = paths.get(key, Path(info["target"]))
                if not info["private"] and info["kind"] in ("memory", "user"):
                    self.memory_diff(key, old, new, day, self.since, path.name)
                    continue
                matches = self.writes(path, self.since, self.now) if not info["private"] else []
                call = matches[-1] if matches else None
                self.state.put(self.item(f"{key}:{day}", day, info["kind"],
                    call["profile"] if call else info["bot"], info["target"],
                    self.state.content(old["blob"]), self.state.content(new["blob"]),
                    actor="chat" if call else "unknown",
                    basis=self.basis(call["profile"], call["session"], call["at"]) if call else [],
                    private=info["private"], flags=["deleted"] if key not in current else [],
                    ops=len(matches) or 1, undo={"how": "snapshot"}))
        # Store snapshots in the same final commit as the cursor, so a crash cannot
        # consume the baseline before its items are durably written.
        self.state.cursor.update(snapshot=current, snapshot_day=day, snapshot_at=self.now)
