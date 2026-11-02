"""Persisted heart records, parsed at the file boundary."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field


class Record(BaseModel):
    model_config = ConfigDict(frozen=True)


class Evidence(Record):
    message_id: int
    session_id: str


class Entry(Record):
    ts: str
    event: str
    feeling: str
    note: str
    evidence: tuple[Evidence, ...]
    strength: int
    via: Literal["turn", "review"] = "turn"


class Change(Record):
    ts: str
    added: tuple[str, ...] = ()
    removed: tuple[str, ...] = ()
    evidence: tuple[str, ...] = ()
    refused: str | None = None
    undo: str | None = None
    changed: int | None = None


class Message(Record):
    message_id: int
    session_id: str
    role: str
    source: str
    content: str
    timestamp: float


class Reply(Record):
    message_id: int
    text: str


class Candidate(Record):
    message_id: int
    session_id: str
    time: str
    text: str
    reply: Reply | None


class Settings(Record):
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
