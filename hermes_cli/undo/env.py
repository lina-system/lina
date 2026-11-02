"""Root config and live profile homes, resolved once per undo command.

Settings live in the LINA root config.yaml, for example::

    undo:
      rules: [~/Desk/rules/*.md]

No host-specific profile list, schedule, or private paths are shipped.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from pydantic import Field

from hermes_cli.undo.models import Model
from hermes_cli.undo.storage import Store


class Settings(Model):
    rules: list[str] = Field(default_factory=list)


@dataclass(frozen=True, slots=True)
class Env:
    profiles: dict[str, Path]
    settings: Settings


def load(store: Store) -> Env:
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    from hermes_cli.profiles import get_profile_dir, list_profile_names

    path = store.base / "config.yaml"
    config = read_config_doc_readonly(path) if config_exists(path) else {}
    settings = Settings.model_validate(config.get("undo", {}))
    return Env({name: get_profile_dir(name) for name in list_profile_names()}, settings)
