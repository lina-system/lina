"""``lina store owner-backfill``: the owner speaker kind on user turns stored before lina#324 (lina#755).

Migration 0013 left every user turn it found ``unknown``: its route is not in the row. Each profile's ``state.db``
still holds the session source, so the rule ``lina heart`` used before rows had a kind decides them again
(``evidence.owner_before_speakers``). Only ``unknown`` rows change; a decided kind is never touched, so a second run
changes nothing. Without ``--apply`` the command only counts.
"""

from __future__ import annotations

import sqlite3
from contextlib import closing
from dataclasses import dataclass
from pathlib import Path

from hermes_state_store import MessageStore, agent_id_for

# The agent's undecided user turns; row-level security keeps them to the store's agent.
_UNDECIDED = ("SELECT uid, session_id, role, coalesce(content, '') FROM memory.messages"
              " WHERE source = 'agent' AND role = 'user' AND speaker = 'unknown'")


@dataclass(frozen=True)
class Counts:
    agent: str
    unknown: int
    owner: int
    set: int

    def line(self, *, apply: bool) -> str:
        action = f"set {self.set}" if apply else "would set them (run with --apply)"
        return (f"{self.agent}: {self.unknown} user turns of unknown speaker, {self.owner} the owner's; {action}; "
                f"{self.unknown - self.owner} stay unknown")


def session_sources(db_path: Path) -> dict[str, str]:
    with closing(sqlite3.connect(db_path.resolve().as_uri() + "?mode=ro", uri=True, timeout=5)) as database:
        return {session: source for session, source in database.execute("SELECT id, source FROM sessions")
                if source}


def backfill_profile(db_path: Path, owner_sources: tuple[str, ...], *, apply: bool) -> Counts:
    from hermes_cli.heart.evidence import owner_before_speakers

    sources = session_sources(db_path)
    with closing(MessageStore(agent_id_for(db_path))) as store:
        with store._cursor() as cur:
            cur.execute(_UNDECIDED)
            undecided = cur.fetchall()
        owners = [uid for uid, session, role, content in undecided
                  if owner_before_speakers(role, sources.get(session), content, owner_sources)]
        changed = 0
        if apply and owners:
            with store.write(), store._writer_cursor() as cur:
                # The sender's handle was never stored, so the ref stays empty.
                cur.execute("UPDATE memory.messages SET speaker = 'owner', speaker_ref = NULL"
                            " WHERE uid = ANY(%s) AND speaker = 'unknown'", (owners,))
                changed = cur.rowcount
        return Counts(store.agent_id, len(undecided), len(owners), changed)


def run(args) -> int:
    from hermes_cli.heart import storage
    from hermes_cli.profiles import list_profiles

    owner_sources = storage.settings().owner_sources
    for profile in list_profiles(lazy_skill_count=True):
        db_path = profile.path / "state.db"
        if db_path.exists():
            print(backfill_profile(db_path, owner_sources, apply=args.apply).line(apply=args.apply))
    return 0
