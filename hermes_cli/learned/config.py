"""Root-owned learned settings; private selectors never live in environment variables.

learned: {profiles: [default, amber], snapshot_time: '23:40', rules: [],
          private: [], send_target: 'telegram', send_profile: default,
          send_retry_seconds: 600, llm_command: ''}
Locale, owner and agent names come from work's root configuration.
"""
from __future__ import annotations

import fnmatch
import glob
from pathlib import Path
from pydantic import BaseModel, ConfigDict, Field
from hermes_cli.work.env import bind, current, load


class Settings(BaseModel):
    model_config = ConfigDict(frozen=True, extra="forbid")
    profiles: tuple[str, ...] | None = None
    snapshot_time: str = "23:40"
    rules: tuple[str, ...] = ()
    private: tuple[str, ...] = ()
    send_target: str = ""
    send_profile: str = "default"
    send_retry_seconds: float = Field(default=600, ge=0)
    llm_command: str = ""


class MissingProfiles(ValueError):
    def __init__(self, names: list[str]):
        self.names = names
        super().__init__("missing profile sources")


class Config:
    def __init__(self):
        from hermes_cli.profiles import get_profile_dir, list_profile_names
        bind(load())
        env = current()
        settings = Settings.model_validate(env.config.get("learned") or {})
        self.home = env.base
        names = settings.profiles if settings.profiles is not None else list_profile_names()
        self.profiles = {name: get_profile_dir(name) for name in names}
        self.owner = env.owner
        self.desk = env.desk
        self.ledger = env.root / "work.db"
        self.state = env.base / "learned"
        self.cutoff = settings.snapshot_time
        self.hour, self.minute = map(int, self.cutoff.split(":"))
        if not (0 <= self.hour <= 23 and 0 <= self.minute <= 59):
            raise ValueError("invalid snapshot time")
        self.rules, self.private = settings.rules, settings.private
        self.llm_cmd = settings.llm_command
        self.send_target, self.send_profile = settings.send_target, settings.send_profile
        self.retry_seconds = settings.send_retry_seconds

    def validate_profiles(self) -> None:
        missing = [name for name, home in self.profiles.items()
                   if not home.is_dir() or not (home / "state.db").is_file()]
        if missing:
            raise MissingProfiles(missing)

    def is_private(self, kind, bot, target="", path=None, text=""):
        candidates = [target, kind + ":" + bot, kind + ":" + bot + ":" + target]
        if path is not None:
            candidates.extend((str(path), str(path.resolve())))
        for entry in self.private:
            if entry.startswith("text:"):
                # text:<kind>:<profile>:<substring>, for private memory entries.
                parts = entry.split(":", 3)
                if len(parts) == 4 and fnmatch.fnmatchcase(kind, parts[1]) and fnmatch.fnmatchcase(bot, parts[2]):
                    if parts[3] and parts[3] in text:
                        return True
                continue
            pattern = str(Path(entry).expanduser()) if entry.startswith(("~", "/")) else entry
            if any(fnmatch.fnmatchcase(candidate, pattern) or candidate.startswith(pattern.rstrip("/") + "/")
                   for candidate in candidates):
                return True
        return False

    def rule_files(self):
        paths = set()
        # Shared skill snapshots cover edits that have no curator ledger entry.
        patterns = list(self.rules) + [str(self.home / "shared-skills/**/SKILL.md")]
        for pattern in patterns:
            expanded = str(Path(pattern).expanduser())
            for name in glob.glob(expanded, recursive=True):
                path = Path(name).absolute()
                if path.is_file():
                    paths.add(path)
        return sorted(paths)
