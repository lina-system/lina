"""Curator ledger net skill changes and confirmed conclusion calls."""
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
from hermes_cli.learned.upstream import skill_source, skill_blob, skill_facts

class SkillCollection:
    def skills(self):
        groups = self.state.cursor.setdefault("skills", {})
        for profile, home in self.config.profiles.items():
            usage, nodes = skill_facts(home)
            for event, line_hash in self.state.lines(skill_source(home), ready=self.stream_ready):
                at = messages.timestamp(event.get("ts"))
                day = self.day(at)
                before = {entry["path"]: entry["sha256"] for entry in event.get("before", [])}
                after = {entry["path"]: entry["sha256"] for entry in event.get("after", [])}
                for path in sorted(before.keys() | after.keys()):
                    skill = str(event.get("skill", ""))
                    key = storage.digest(f"skill:{day}:{profile}:{skill}:{path}")
                    group = groups.get(key)
                    event_key = storage.digest(str(event.get("id") or line_hash))
                    if group and event_key in group["events"]:
                        continue
                    private = (self.config.is_private("skill", profile, skill, home / "skills" / path)
                               or self.config.is_private("skill", profile, skill, home / "skills" / skill / path)
                               or self.config.is_private("skill", profile, skill, home / "skills" / skill))
                    contents, lost = {}, False
                    for side, entries in (("before", before), ("after", after)):
                        sha = entries.get(path)
                        contents[side] = ""
                        if sha and not private:
                            if not re.fullmatch(r"[0-9a-f]{64}", sha):
                                raise ValueError("invalid skill blob digest")
                            raw = skill_blob(home, sha)
                            if raw is None:
                                lost = True
                                continue
                            if storage.digest(raw) != sha:
                                raise ValueError("skill blob digest mismatch")
                            # Retain every intermediate blob, with secrets masked at ingestion.
                            contents[side] = self.state.blob(mask(raw.decode("utf-8", errors="replace")))
                    if group is None:
                        group = {"events": [], "first": contents["before"], "actors": {}, "basis": [],
                                 "entries": [], "flags": [], "private": private}
                    group["events"].append(event_key)
                    group["last"] = contents["after"]
                    who = str(event.get("actor", "unknown"))
                    # Keep source names for the curator/agent split, normalized actor in the item.
                    who = who if who in ("curator", "agent", "user") else "unknown"
                    group["actors"][who] = group["actors"].get(who, 0) + 1
                    if lost:
                        group["flags"].append("content_lost")
                    group["flags"] = [flag for flag in group["flags"] if flag != "deleted"]
                    if path not in after:
                        group["flags"].append("deleted")
                    if not private:
                        for basis in self.basis(profile, event.get("evidence", {}).get("session_id"), at):
                            if basis not in group["basis"]:
                                group["basis"].append(basis)
                        group["entries"].append(str(event.get("id", "")))
                    actor = {"curator": "background", "agent": "chat", "user": "cli"}.get(who, "unknown")
                    row = self.item(key, day, "skill", profile, "skill:" + skill + ":" + path,
                                    self.state.content(group["first"]), self.state.content(group["last"]),
                                    actor=actor, basis=group["basis"], private=private,
                                    flags=group["flags"], ops=len(group["events"]),
                                    undo={"how": "owner", "entries": group["entries"]})
                    row["actors"] = group["actors"]
                    if not private:
                        row["op_ids"] = ["E" + event[:16] for event in group["events"]]
                        first, last = self.state.content(group["first"]), self.state.content(group["last"])
                        row["net_size"] = display.net_size(first, last)
                        manifest = home / "skills" / skill / "SKILL.md"
                        description = last or first if Path(path).name == "SKILL.md" else ""
                        if not description and manifest.is_file() and not self.config.is_private(
                                "skill", profile, skill, manifest):
                            description = mask(manifest.read_text(encoding="utf-8-sig"))
                        row["display_name"] = display.skill_name(description)
                        node = nodes.get(skill)
                        row["skill_usage"] = {k: usage.get(skill, {}).get(k) for k in
                                              ("use_count", "patch_count", "state", "created_by")}
                        if node is not None:
                            row["related_skills"] = node.related
                    groups[key] = group
                    self.state.put(row)
    def conclusions(self):
        pending = []
        for call in self.calls:
            if call["name"] != "honcho_conclude":
                continue
            args, result = call["args"], call["result"]
            key, day = call["key"], self.day(call["at"])
            peer = args.get("observed_id", args.get("observed", args.get("peer", self.config.owner)))
            if isinstance(result, dict):
                peer = result.get("observed_id", result.get("observed", peer))
            private = peer != self.config.owner or self.config.is_private("conclusion", call["profile"])
            deleted = bool(args.get("delete_id"))
            after = str(args.get("conclusion", ""))
            if not deleted and not after:
                continue
            row = self.item(key, day, "conclusion", call["profile"], "conclusion:" + str(args.get("delete_id", "new")),
                            after=after, actor="chat", basis=self.basis(call["profile"], call["session"], call["at"]),
                            source="seen" if messages.confirmed(result) else "bot_said", private=private,
                            flags=["deleted"] if deleted else [], undo={"how": "owner" if deleted else "honcho_delete"})
            if messages.failed(result):
                self.state.remove(day, row["id"])
            else:
                self.state.put(row)
            if not messages.confirmed(result) and not messages.failed(result):
                pending.append(call["at"])
        # Keep unresolved calls eligible for later result joins, including across days.
        self.state.cursor["calls_since"] = min(pending) if pending else self.now
