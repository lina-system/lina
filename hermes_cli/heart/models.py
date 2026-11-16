"""Persisted heart records, parsed at the file boundary."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


class Record(BaseModel):
    model_config = ConfigDict(frozen=True)


class Pref(Record):
    """The taste a journal entry bears on (docs/design/agent-tastes.md); ``route`` decides the owner rule."""
    target: str
    kind: str
    dir: str
    route: str


class Entry(Record):
    """One journal line; ``evidence`` are the ``memory.messages`` uids it cites."""
    ts: str
    event: str
    feeling: str
    note: str
    evidence: tuple[str, ...]
    strength: int
    via: Literal["turn", "review"] = "turn"
    pref: Pref | None = None

    @property
    def reaction(self) -> bool:
        return self.pref is None or self.pref.route == "reaction"


class Change(Record):
    ts: str
    added: tuple[str, ...] = ()
    removed: tuple[str, ...] = ()
    evidence: tuple[str, ...] = ()
    refused: str | None = None
    undo: str | None = None
    changed: int | None = None


class Message(Record):
    uid: str
    session_id: str
    role: str
    source: str
    content: str
    timestamp: float


class Reply(Record):
    uid: str
    text: str


class Candidate(Record):
    uid: str
    session_id: str
    time: str
    text: str
    reply: Reply | None


class Settings(Record):
    # Read only by `lina store owner-backfill` (lina#755): rows now store their speaker kind (lina#324).
    owner_sources: tuple[str, ...] = ("telegram", "desktop", "tui", "webui", "imsg_mac", "cli")
    weekly_out_dir: str | None = None


class Insight(Record):
    profile: str
    open: int = 0
    closed: int = 0
    answered: int = 0
    correct: int = Field(default=0, validation_alias="맞다")
    incorrect: int = Field(default=0, validation_alias="틀리다")
    unknown: int = Field(default=0, validation_alias="모르겠다")
    accuracy: float | None = None
