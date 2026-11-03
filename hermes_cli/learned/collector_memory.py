"""Memory entry transitions attributed to confirmed conversation calls."""
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

class MemoryCollection:
    def file_memory_calls(self, bot, filename):
        return [call for call in self.calls if call["name"] == "memory" and call["profile"] == bot
                and str(call["args"].get("target", "memory")).lower().removesuffix(".md") == filename.lower().removesuffix(".md")
                and messages.confirmed(call["result"])]

    def memory_usage(self, bot, filename, length):
        # No invented capacity: tool percentages/limits are optional evidence.
        for call in reversed(self.file_memory_calls(bot, filename)):
            result = call["result"]
            percent = result.get("usage_percent")
            if isinstance(percent, (int, float)) and 0 <= percent <= 100:
                return percent
            limit = result.get("char_limit")
            if isinstance(limit, (int, float)) and limit > 0:
                return round(100 * length / limit, 1)
        return None

    def memory_calls(self, bot, filename, before, after, since):
        edges = []
        for call in self.calls:
            if call["name"] != "memory" or call["profile"] != bot or call["at"] < since:
                continue
            args, result = call["args"], call["result"]
            if not isinstance(result, dict) or messages.failed(result):
                continue
            target = str(args.get("target", "memory")).lower().removesuffix(".md")
            if target != filename.lower().removesuffix(".md"):
                continue
            action = args.get("action")
            old = result.get("patched_entry_before", result.get("replaced_entry", result.get("removed_entry", "")))
            new = result.get("patched_entry_after", args.get("content", args.get("new_text", "")))
            if action == "remove":
                new = ""
            if action == "add":
                old = ""
            if not isinstance(old, str) or not isinstance(new, str):
                continue
            edges.append((mask(old).strip(), mask(new).strip(), call))
        # Walk exact content transitions in time order, including multiple rewrites.
        current, matched = before, []
        for old, new, call in edges:
            if old == current and old != new:
                current = new
                matched.append(call)
                if current == after:
                    return matched
        # A dissimilar replacement appears as one removed + one added entry.
        # Each side is still attributable to its exact tool-result transition.
        if before and not after:
            return [call for old, new, call in edges if old == before and new != old]
        if after and not before:
            return [call for old, new, call in edges if new == after and new != old]
        return []

    def memory_diff(self, key, old, new, day, since, filename):
        left, right = list(old["entries"]), list(new["entries"])
        for entry in list(left):
            if entry in right:
                left.remove(entry)
                right.remove(entry)
        pairs = []
        scores = sorted(((difflib.SequenceMatcher(None, a.get("text", ""), b.get("text", "")).ratio(), i, j)
                         for i, a in enumerate(left) for j, b in enumerate(right)
                         if not a["private"] and not b["private"]), reverse=True)
        used_left, used_right = set(), set()
        for score, i, j in scores:
            if score >= 0.6 and i not in used_left and j not in used_right:
                pairs.append((left[i], right[j]))
                used_left.add(i)
                used_right.add(j)
        pairs.extend((a, None) for i, a in enumerate(left) if i not in used_left)
        pairs.extend((None, b) for j, b in enumerate(right) if j not in used_right)
        for index, (a, b) in enumerate(pairs):
            private = bool((a or {}).get("private") or (b or {}).get("private"))
            before, after = (a or {}).get("text", ""), (b or {}).get("text", "")
            matched = self.memory_calls(new["bot"], filename, before, after, since) if not private else []
            basis = []
            for call in reversed(matched):
                for record in self.basis(call["profile"], call["session"], call["at"]):
                    if record not in basis:
                        basis.append(record)
            row = self.item(f"{key}:{day}:{index}", day, new["kind"], new["bot"], new["target"],
                            before, after, actor="chat" if matched else "unknown", basis=basis,
                            private=private, flags=["deleted"] if b is None else [], ops=len(matched) or 1)
            row["change"] = "removed" if b is None else "added" if a is None else "changed"
            if not private:
                row["file_ops"] = sum(call["at"] > since for call in self.file_memory_calls(new["bot"], filename))
                row["usage"] = {"before": old.get("usage"), "after": new.get("usage")}
            self.state.put(row)
