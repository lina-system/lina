"""Daily snapshot: journal agent memory and rule files changed outside ``begin``/``end``.

Upstream checkpoints copy a working directory only before an agent's own file tool writes there, and only
with ``checkpoints.enabled``; memory-tool writes, curator runs and hand edits never take one. The snapshot keeps
the last captured copy of each file in ``snapshot.json`` and journals the files that changed since then as one
revert entry per agent, so ``lina undo apply`` restores the earlier copy. A file's first run only records its copy.
"""

from __future__ import annotations

import glob
import json
from pathlib import Path

from hermes_cli.undo import copies, journal
from hermes_cli.undo.env import Env
from hermes_cli.undo.models import Entry, FileRef
from hermes_cli.undo.pending import HOW, changed_ref, current_hash, file_type
from hermes_cli.undo.storage import Store, atomic, read_bytes

RULES_BOT = "lina-rules"


def targets(env: Env) -> dict[str, list[Path]]:
    files = {name: [home / "memories/MEMORY.md", home / "memories/USER.md", home / "SOUL.md"]
             for name, home in env.profiles.items()}
    rules = sorted({Path(p) for pattern in env.settings.rules
                    for p in glob.glob(str(Path(pattern).expanduser().absolute()))})
    if rules:
        files[RULES_BOT] = rules
    return files


def baseline_path(store: Store) -> Path:
    return store.root / "snapshot.json"


def read_baseline(store: Store) -> dict:
    data = read_bytes(baseline_path(store))
    return json.loads(data) if data is not None else {"at": None, "files": {}}


def plan(store: Store, env: Env) -> dict[str, tuple[list[Path], list[Path]]]:
    """Per bot: (files changed since their last copy, files without a copy yet)."""
    saved = read_baseline(store)["files"]
    result = {}
    for bot, paths in targets(env).items():
        changed = [p for p in paths if str(p) in saved and current_hash(p) != saved[str(p)]["sha256"]]
        new = [p for p in paths if str(p) not in saved]
        if changed or new:
            result[bot] = (changed, new)
    return result


def dry_run(store: Store, env: Env) -> None:
    pending = plan(store, env)
    for bot, (changed, new) in pending.items():
        for path in changed:
            print(f"would journal {bot}: {path}")
        for path in new:
            print(f"would record the first copy of {bot}: {path}")
    if not pending:
        print("No changed files since the last snapshot")


def run(store: Store, env: Env) -> None:
    with store.locked():
        baseline = read_baseline(store)
        saved = baseline["files"]
        changes, at = [], None
        for bot, (changed, new) in plan(store, env).items():
            refs = []
            for path in changed:
                old = saved[str(path)]
                ref = changed_ref(store, FileRef(type=file_type(env, path), path=str(path), sha256=old["sha256"],
                                                 after=old["sha256"], checkpoint=old["checkpoint"]))
                if ref is not None:
                    refs.append(ref)
            for path in [*changed, *new]:
                copy = copies.capture(store, path)
                saved[str(path)] = {"sha256": copy.sha256, "checkpoint": copy.checkpoint}
            if refs:
                at, change_id = journal.identity()
                since = f" since {baseline['at']}" if baseline["at"] else ""
                journal.append_locked(store, Entry(at=at, bot=bot, change_id=change_id, how=HOW, objects=refs,
                                                   what=f"Daily snapshot: {len(refs)} files changed{since}"))
                changes.append(f"journaled {bot}: {len(refs)} files, change_id={change_id}")
            if new:
                changes.append(f"recorded the first copy of {len(new)} files of {bot}")
        # Journal first: a crash before this write journals the same change again, never loses one.
        atomic(baseline_path(store), json.dumps({"at": at or journal.identity()[0], "files": saved}).encode())
    print("\n".join(changes) if changes else "No changed files since the last snapshot")
