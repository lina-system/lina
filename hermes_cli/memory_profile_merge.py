"""``lina memory profile merge`` (lina#467): find user profile entries that several agent profiles of
this install repeat, and move them into the shared owner profile (``<root>/shared/OWNER.md``).

Entries are grouped by ``tools.memory_tool_shared``'s rule (equal keys, or keys at least
``NEAR_DUPLICATE_RATIO`` alike). A group whose entries all have one key and that spans two or more
profiles (or a profile and the shared layer) moves: identical when every copy is byte-equal,
near-identical when they differ only in case, spacing, typography or trailing punctuation. Any other
group is diverged, a fact in different words or with a different value, and is listed for the owner
to pick; it is never merged. The command is a dry run unless ``--apply`` is given, and ``--apply``
writes only after the owner types the confirmation word. Every file it rewrites is copied to
``<file>.bak-<UTC timestamp>`` first, and each write runs under the memory store's file lock with
an atomic rename, so an agent writing at the same time keeps its edit."""

from __future__ import annotations

import os
import time
from collections import Counter
from dataclasses import dataclass, field
from pathlib import Path

CONFIRM_WORD = "merge"


@dataclass
class Move:
    wording: str  # what the shared layer keeps
    kind: str  # "identical" | "near-identical"
    holders: list[tuple[str, str]]  # (profile name, the entry as that profile holds it)


@dataclass
class Plan:
    shared_path: Path
    shared_entries: list[str]
    profiles: dict[str, Path]  # profile name -> its USER.md
    entries: dict[str, list[str]]
    moves: list[Move] = field(default_factory=list)
    diverged: list[list[tuple[str, str]]] = field(default_factory=list)  # (source, entry) per group


def install_profiles() -> list[tuple[str, Path]]:
    from hermes_cli.profiles import list_profiles
    return [(info.name, Path(info.path)) for info in list_profiles(lazy_skill_count=True)]


def _groups(items: list[tuple[str, str]]) -> list[list[int]]:
    """Groups (of two or more) of *items* indices joined transitively by the matching rule."""
    from tools.memory_tool_shared import NEAR_DUPLICATE_RATIO, entry_key, similarity
    keys = [entry_key(entry) for _source, entry in items]
    parent = list(range(len(items)))

    def find(i: int) -> int:
        while parent[i] != i:
            parent[i] = parent[parent[i]]
            i = parent[i]
        return i

    for i in range(len(items)):
        for j in range(i + 1, len(items)):
            if keys[i] == keys[j] or similarity(keys[i], keys[j]) >= NEAR_DUPLICATE_RATIO:
                parent[find(j)] = find(i)
    groups: dict[int, list[int]] = {}
    for i in range(len(items)):
        groups.setdefault(find(i), []).append(i)
    return [members for members in groups.values() if len(members) > 1]


def build_plan(profiles: list[tuple[str, Path]], shared_path: Path) -> Plan:
    """What a merge would move and list, read from the files as they are now (nothing is written)."""
    from tools.memory_tool_shared import entry_key
    from tools.memory_tool_store import MemoryStore
    plan = Plan(shared_path=shared_path, shared_entries=MemoryStore._read_file(shared_path),
                profiles={name: home / "memories" / "USER.md" for name, home in profiles}, entries={})
    for name, path in plan.profiles.items():
        plan.entries[name] = list(dict.fromkeys(MemoryStore._read_file(path)))
    items = [("shared", entry) for entry in plan.shared_entries]
    items += [(name, entry) for name, entries in plan.entries.items() for entry in entries]
    for members in _groups(items):
        group = [items[i] for i in members]
        sources = {source for source, _entry in group}
        if len({entry_key(entry) for _source, entry in group}) > 1 or len(sources) < 2:
            plan.diverged.append(group)
            continue
        shared_copy = next((entry for source, entry in group if source == "shared"), None)
        counts = Counter(entry for _source, entry in group)
        wording = shared_copy or counts.most_common(1)[0][0]  # ties: the first one met
        plan.moves.append(Move(wording, "identical" if len(counts) == 1 else "near-identical",
                               [(source, entry) for source, entry in group if source != "shared"]))
    return plan


def _limits(plan: Plan) -> tuple[int, dict[str, int]]:
    """The shared budget (this profile's config) and each profile's own user budget (its config)."""
    from hermes_cli.config import DEFAULT_CONFIG, load_config_readonly
    from hermes_cli.config_effective import load_user_config_effective
    defaults = DEFAULT_CONFIG["memory"]
    shared = (load_config_readonly().get("memory") or {}).get("shared_user_char_limit",
                                                              defaults["shared_user_char_limit"])
    per_profile = {}
    for name, path in plan.profiles.items():
        memory = load_user_config_effective(path.parent.parent / "config.yaml").get("memory") or {}
        per_profile[name] = int(memory.get("user_char_limit", defaults["user_char_limit"]))
    return int(shared), per_profile


def _merged_shared(plan: Plan, current: list[str]) -> list[str]:
    from tools.memory_tool_shared import entry_key
    present = {entry_key(entry) for entry in current}
    return current + [move.wording for move in plan.moves if entry_key(move.wording) not in present]


def _removals(plan: Plan) -> dict[str, set[str]]:
    removals: dict[str, set[str]] = {}
    for move in plan.moves:
        for name, entry in move.holders:
            removals.setdefault(name, set()).add(entry)
    return removals


def _size(entries: list[str]) -> int:
    from tools.memory_tool_store import ENTRY_DELIMITER
    return len(ENTRY_DELIMITER.join(entries))


def render(plan: Plan) -> str:
    """The plan as the owner reads it: moves, diverged groups, and each layer's budget before and after."""
    from tools.memory_tool_store import MEMORY_LAYER_LABELS
    shared_limit, user_limits = _limits(plan)
    lines = [f"Shared owner profile: {plan.shared_path}", ""]
    lines.append(f"Moves to the shared layer ({len(plan.moves)}):" if plan.moves else "Nothing to move.")
    for move in plan.moves:
        lines.append(f"  [{move.kind}] {move.wording}")
        lines += [f"      from {name}: {entry}" for name, entry in move.holders]
    if plan.diverged:
        lines += ["", f"Diverged, never merged; pick one wording and edit the profiles ({len(plan.diverged)}):"]
        for number, group in enumerate(plan.diverged, 1):
            lines.append(f"  group {number}:")
            lines += [f"      {source}: {entry}" for source, entry in group]
    removals = _removals(plan)
    lines += ["", "Budgets per layer (now -> after merge):",
              f"  {MEMORY_LAYER_LABELS['shared']}: {_size(plan.shared_entries):,} -> "
              f"{_size(_merged_shared(plan, plan.shared_entries)):,} / {shared_limit:,} chars"]
    for name, entries in plan.entries.items():
        after = [entry for entry in entries if entry not in removals.get(name, set())]
        lines.append(f"  {name} {MEMORY_LAYER_LABELS['user']}: {_size(entries):,} -> {_size(after):,} / "
                     f"{user_limits[name]:,} chars")
    return "\n".join(lines)


def _backup(path: Path, raw: bytes) -> Path:
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    for n in range(1000):
        bak = path.with_name(f"{path.name}.bak-{stamp}" + (f".{n}" if n else ""))
        try:
            with open(bak, "xb") as fh:
                fh.write(raw)
        except FileExistsError:
            continue
        os.chmod(bak, path.stat().st_mode & 0o777)
        return bak
    raise RuntimeError(f"no free backup name next to {path}")


def _rewrite(path: Path, rewrite) -> tuple[list[str], list[str], Path | None]:
    """Under the store's lock: read *path*, ``rewrite(entries)``, back the old file up and write the
    new entries atomically when they differ. ``(before, after, backup path or None)``."""
    from tools.memory_tool_store import MemoryStore
    with MemoryStore._file_lock(path):
        raw, read_ok = MemoryStore._read_raw_checked(path)
        if not read_ok:
            raise RuntimeError(f"{path} exists but cannot be read; nothing was written to it")
        before = list(dict.fromkeys(MemoryStore._parse_entries(raw)))
        after = rewrite(before)
        if after == before:
            return before, after, None
        backup = _backup(path, path.read_bytes()) if path.exists() else None
        MemoryStore._write_file(path, after)
        return before, after, backup


def apply(plan: Plan) -> tuple[bool, list[str]]:
    """Write the plan: the shared layer first (so a stop halfway leaves a copy, never a loss), then
    each profile drops the entries that moved. Re-reads every file under its lock, so an entry a
    profile changed after the plan was shown stays. ``(ok, report lines)``."""
    from hermes_constants import mkdir_under_hermes_home
    from tools.memory_tool_store import MEMORY_LAYER_LABELS
    shared_limit, _user_limits = _limits(plan)
    full: list[int] = []

    def _shared(current: list[str]) -> list[str]:
        merged = _merged_shared(plan, current)
        if _size(merged) > shared_limit:
            full.append(_size(merged))
            return current
        return merged

    mkdir_under_hermes_home(plan.shared_path.parent)
    before, after, backup = _rewrite(plan.shared_path, _shared)
    if full:
        return False, [f"The {MEMORY_LAYER_LABELS['shared']} is full: the merge would put it at {full[0]:,}/"
                       f"{shared_limit:,} chars. Nothing was changed; shorten or remove shared entries, or raise "
                       f"memory.shared_user_char_limit, then run the merge again."]
    lines = [f"{MEMORY_LAYER_LABELS['shared']}: {len(after) - len(before)} entries added"
             + (f" (backup {backup})" if backup else "")]
    for name, gone in _removals(plan).items():
        path = plan.profiles[name]
        before, after, backup = _rewrite(path, lambda entries, gone=gone: [e for e in entries if e not in gone])
        kept = sorted(gone - set(before))
        lines.append(f"{name}: {len(before) - len(after)} entries removed" + (f" (backup {backup})" if backup else ""))
        lines += [f"  {name} changed this entry since the plan, so it stays: {entry}" for entry in kept]
    return True, lines


def merge_command(args) -> int:
    from tools.memory_tool import get_shared_memory_dir
    from tools.memory_tool_shared import SHARED_FILENAME
    plan = build_plan(install_profiles(), get_shared_memory_dir() / SHARED_FILENAME)
    print(render(plan))
    if not getattr(args, "apply", False):
        print("\nDry run: nothing was changed. Run again with --apply to move the entries above.")
        return 0
    if not plan.moves:
        return 0
    try:
        answer = input(f'\nType "{CONFIRM_WORD}" to move {len(plan.moves)} entries into the shared owner profile: ')
    except (EOFError, KeyboardInterrupt):
        answer = ""
    if answer.strip() != CONFIRM_WORD:
        print("Not confirmed: nothing was changed.")
        return 1
    ok, lines = apply(plan)
    print("\n".join(lines))
    return 0 if ok else 1
