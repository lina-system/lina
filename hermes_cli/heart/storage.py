"""Private heart files and the command clock."""

from __future__ import annotations

import datetime as dt
import json
import os
import re
import tempfile
import time
from pathlib import Path
from typing import TypeVar

from pydantic import ValidationError

from hermes_cli.heart.locale import text
from hermes_cli.heart.models import Record, Settings
from hermes_cli.work.env import current
from hermes_constants import get_hermes_home

T = TypeVar("T", bound=Record)


class HeartError(Exception):
    """An actionable command error."""


def now() -> float:
    return time.time()


def home() -> Path:
    return get_hermes_home()


def settings() -> Settings:
    return Settings.model_validate(current().section("heart"))


def stamp(seconds: float | None = None) -> str:
    return dt.datetime.fromtimestamp(now() if seconds is None else seconds).astimezone().isoformat(timespec="seconds")


def parse_ts(value: str) -> dt.datetime | None:
    try:
        parsed = dt.datetime.fromisoformat(value)
        return parsed.astimezone() if parsed.tzinfo is None else parsed
    except ValueError:
        return None


def day(value: str | None = None) -> dt.date:
    if value is None:
        return dt.datetime.fromtimestamp(now()).astimezone().date()
    try:
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", value):
            raise ValueError(value)
        return dt.date.fromisoformat(value)
    except ValueError as error:
        raise HeartError(text("date", value=value)) from error


def since(value: str) -> dt.datetime:
    relative = re.fullmatch(r"(\d+)d", value)
    parsed = parse_ts(stamp(now() - int(relative[1]) * 86400) if relative else value)
    if parsed is None:
        raise HeartError(text("time", value=value))
    return parsed


def private_dir(path: Path) -> Path:
    path.mkdir(mode=0o700, parents=True, exist_ok=True)
    path.chmod(0o700)
    return path


def append(path: Path, record: Record) -> None:
    private_dir(path.parent)
    descriptor = os.open(path, os.O_WRONLY | os.O_APPEND | os.O_CREAT, 0o600)
    with os.fdopen(descriptor, "a", encoding="utf-8") as stream:
        os.fchmod(stream.fileno(), 0o600)
        stream.write(record.model_dump_json(exclude_unset=True) + "\n")


def records(path: Path, model: type[T]) -> list[T]:
    if not path.exists():
        return []
    result = []
    for line in path.read_text(encoding="utf-8-sig").splitlines():
        if not line.strip():
            continue
        try:
            result.append(model.model_validate_json(line))
        except ValidationError:
            continue  # An interrupted or malformed historical line is not evidence.
    return result


def load_text(path: Path) -> str:
    return path.read_text(encoding="utf-8-sig") if path.exists() else ""


def write_private(path: Path, value: str) -> None:
    private_dir(path.parent)
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, delete=False) as stream:
        temporary = Path(stream.name)
        stream.write(value)
    temporary.replace(path)


def json_print(value: list[Record]) -> None:
    print(json.dumps([row.model_dump(mode="json") for row in value], ensure_ascii=False))
