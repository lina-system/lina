"""Strict journal reference schemas; object bodies never enter the journal."""

from __future__ import annotations

from datetime import datetime
from typing import Annotated, Literal, assert_never

from pydantic import BaseModel, ConfigDict, Field, ValidationInfo, field_validator, model_validator

from hermes_cli.work.common import mask

Token = Annotated[str, Field(pattern=r"^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,199}$")]
Sha = Annotated[str, Field(pattern=r"^[0-9a-f]{64}$")]
Commit = Annotated[str, Field(pattern=r"^[0-9a-f]{40,64}$")]


class UndoError(Exception):
    """Content-free operator diagnostic."""


class Model(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)


class Shrunk(Model):
    before: Annotated[int, Field(gt=0)]
    after: Annotated[int, Field(ge=0)] | None


class FileRef(Model):
    type: Literal["memory", "rule", "file"]
    path: str
    sha256: Sha | None
    after: Sha | None
    checkpoint: Commit | None = None
    shrunk: Shrunk | None = None

    @field_validator("path")
    @classmethod
    def absolute_path(cls, value: str) -> str:
        from pathlib import Path

        if not Path(value).is_absolute() or ".." in Path(value).parts or any(ord(c) < 32 for c in value):
            raise UndoError("file references require absolute paths")
        return value

    @model_validator(mode="after")
    def copy_reference(self) -> FileRef:
        if self.sha256 is not None and self.checkpoint is None:
            raise UndoError("existing files require a checkpoint commit reference")
        return self


class SkillRef(Model):
    type: Literal["skill"]
    profile: Token
    entry_id: Token


class LedgerRef(Model):
    type: Literal["ledger"]
    id: Annotated[str, Field(pattern=r"^[AGTM][1-9][0-9]*$")]
    rev: Annotated[int, Field(gt=0)]
    after: Annotated[int, Field(gt=0)]


class OutsideRef(Model):
    type: Literal["outside"]
    service: Token
    id: Token
    revision: Token | None = None
    copy_sha256: Sha | None = None


Reference = Annotated[FileRef | SkillRef | LedgerRef | OutsideRef, Field(discriminator="type")]


def when(value: str) -> float:
    parsed = datetime.fromisoformat(value)
    if parsed.tzinfo is None:
        raise UndoError("timestamps must include a timezone")
    return parsed.timestamp()


class Entry(Model):
    at: str
    bot: Token
    change_id: Token
    what: str
    objects: Annotated[list[Reference], Field(min_length=1, max_length=100)]
    kind: Literal["revert", "compensate", "none"] = "revert"
    until: str = ""
    how: str
    undo_of: Token | None = None

    @field_validator("what", "how")
    @classmethod
    def plain(cls, value: str, info: ValidationInfo) -> str:
        limit = 240 if info.field_name == "what" else 500
        if not value.strip() or len(value) > limit or any(ord(c) < 32 for c in value):
            raise UndoError("descriptions must be one short plain line, without contents")
        return mask(value.strip())

    @field_validator("at", "until")
    @classmethod
    def timestamp(cls, value: str) -> str:
        if value:
            when(value)
        return value

    @model_validator(mode="after")
    def unique_objects(self) -> Entry:
        identities = []
        for ref in self.objects:
            match ref:
                case FileRef():
                    identity = ("file", ref.path)
                case SkillRef():
                    identity = ("skill", ref.profile, ref.entry_id)
                case LedgerRef():
                    identity = ("ledger", ref.id)
                case OutsideRef():
                    identity = ("outside", ref.service, ref.id)
                case unreachable:
                    assert_never(unreachable)
            identities.append(identity)
        if len(set(identities)) != len(identities):
            raise UndoError("duplicate object in change batch")
        return self
