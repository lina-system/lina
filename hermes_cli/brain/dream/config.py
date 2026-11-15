"""``brain.dream`` in config.yaml (defaults in hermes_cli/config_defaults.py): the per-phase budgets, the report
folder and the cycle lock's lifetime."""

from __future__ import annotations

from dataclasses import dataclass
from datetime import timedelta
from pathlib import Path

from hermes_cli.brain.store import BrainStoreError

DETERMINISTIC = ("upkeep", "orphans")
MODEL_PHASES = ("atoms", "patterns", "concepts", "consolidate", "contradictions", "drift", "promote")
PHASES = DETERMINISTIC + MODEL_PHASES
_COUNTS = ("lock_ttl_minutes", "page_chars", "pattern_days", "concept_min_group", "consolidate_min_atoms",
           "consolidate_min_age_hours")


class DreamError(BrainStoreError):
    pass


@dataclass(frozen=True)
class Budget:
    calls: int
    input_chars: int


@dataclass(frozen=True)
class Settings:
    budgets: dict[str, Budget]
    report_dir: Path
    lock_ttl: timedelta
    page_chars: int
    pattern_days: int
    concept_min_group: int
    consolidate_min_atoms: int
    consolidate_min_age: timedelta


def _defaults() -> dict:
    from hermes_cli.config_defaults import DEFAULT_CONFIG
    return DEFAULT_CONFIG["brain"]["dream"]


def _count(value, where: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise DreamError(f"brain.dream.{where} must be a whole number of 0 or more, not {value!r}")
    return value


def settings(section: dict | None, home: Path) -> Settings:
    """Read ``brain.dream``; a key it leaves out takes its default."""
    section = section or {}
    if not isinstance(section, dict) or not isinstance(section.get("budget") or {}, dict):
        raise DreamError("brain.dream and brain.dream.budget must be mappings")
    defaults = _defaults()
    given = section.get("budget") or {}
    unknown = sorted(set(given) - set(MODEL_PHASES))
    if unknown:
        raise DreamError(f"brain.dream.budget names no model phase: {', '.join(unknown)}")
    budgets = {}
    for phase in MODEL_PHASES:
        spec = {**defaults["budget"][phase], **(given.get(phase) or {})}
        budgets[phase] = Budget(_count(spec["calls"], f"budget.{phase}.calls"),
                                _count(spec["input_chars"], f"budget.{phase}.input_chars"))
    counts = {key: _count(section.get(key, defaults[key]), key) for key in _COUNTS}
    report_dir = Path(section.get("report_dir") or home / "brain-dream").expanduser()
    return Settings(budgets, report_dir, timedelta(minutes=counts["lock_ttl_minutes"]), counts["page_chars"],
                    counts["pattern_days"], counts["concept_min_group"], counts["consolidate_min_atoms"],
                    timedelta(hours=counts["consolidate_min_age_hours"]))


def load(home: Path) -> Settings:
    """The settings of the active profile's config.yaml."""
    from hermes_cli.config import load_config
    return settings((load_config().get("brain") or {}).get("dream"), home)
