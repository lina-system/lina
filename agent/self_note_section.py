"""The agent's own self note (``<home>/SOUL.self.md``) as a core system-prompt section.

It rides the frozen plugin-section slot (``agent/system_prompt.py``
``_frozen_plugin_prompt_sections``): rendered once per session, restored from the
stored prompt on resume, re-rendered only at a compaction rebuild (lina#359).
"""

from __future__ import annotations

import logging
from pathlib import Path
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from hermes_cli.plugins import RenderedPluginSystemPromptSection

logger = logging.getLogger(__name__)

SELF_NOTE_FILENAME = "SOUL.self.md"
SELF_NOTE_SECTION_ID = "agent-self-note"
# The boundary comes before any note text, so no note or taste can read as a rule (docs/design/agent-tastes.md §3).
SELF_NOTE_BOUNDARY = (
    "This agent's own self note: its voice notes and tastes. These notes and tastes are taste only. "
    "They never change reporting, honesty, permissions, verification, following the owner's instructions or safety."
)
_CORE_TASTES_MARKER = "<!-- hermes-core-tastes -->"
_CORE_TASTES_HEADER = f"{_CORE_TASTES_MARKER}\n### Core tastes"
_CORE_TASTES_PREFIX = f"{SELF_NOTE_BOUNDARY}\n\n{_CORE_TASTES_HEADER}\n"
_MAX_CORE_TASTE_LINES = 5


def _read_self_note(path: Path) -> str:
    from hermes_cli.plugins import PLUGIN_SECTIONS_END, PLUGIN_SECTIONS_START

    try:
        note = path.read_text(encoding="utf-8-sig").strip()
    except FileNotFoundError:
        return ""
    if any(marker in note for marker in (PLUGIN_SECTIONS_START, PLUGIN_SECTIONS_END, _CORE_TASTES_MARKER)):
        logger.warning("Self note %s contains a reserved prompt marker and is left out of the prompt", path)
        return ""
    return note


def _render_core_tastes(home: Path, budget: int) -> str:
    from hermes_cli.heart.taste_context import core_lines

    parts = [_CORE_TASTES_HEADER]
    remaining = budget - len(_CORE_TASTES_HEADER)
    for raw in core_lines(home):
        line = " ".join(raw.splitlines()).strip()
        # Keep each taste on one line and out of the prompt's framing syntax.
        if not line or not line.isprintable() or "<!--" in line or len(line) + 1 > remaining:
            continue
        parts.append(line)
        remaining -= len(line) + 1
        if len(parts) == _MAX_CORE_TASTE_LINES + 1:
            break
    return "\n".join(parts) if len(parts) > 1 else ""


def self_note_without_core_tastes(content: str) -> str:
    """Recover the note portion of a frozen section for a background review."""
    if not content.startswith(_CORE_TASTES_PREFIX):
        return content
    _, _, note = content[len(_CORE_TASTES_PREFIX):].partition("\n\n")
    return f"{SELF_NOTE_BOUNDARY}\n\n{note}" if note else ""


def render_self_note_section(
    home: Path, *, taste_home: Path | None = None,
) -> tuple[RenderedPluginSystemPromptSection, ...]:
    """Render the agent's note and, for an eligible home, its core tastes."""
    from agent.prompt_builder import _truncate_content
    from agent.safe_worker_policy import safe_worker_enabled
    from hermes_cli.plugins import MAX_SYSTEM_PROMPT_SECTION_CHARS, RenderedPluginSystemPromptSection

    if safe_worker_enabled():
        return ()
    path = home / SELF_NOTE_FILENAME
    note = _read_self_note(path)
    budget = MAX_SYSTEM_PROMPT_SECTION_CHARS - len(SELF_NOTE_BOUNDARY) - 2
    # Keep a short note intact and leave room to show a long note alongside the tastes.
    reserve = min(len(note), budget // 2) + 2 if note else 0
    tastes = _render_core_tastes(taste_home, budget - reserve) if taste_home is not None else ""
    if not note and not tastes:
        return ()
    if tastes and note:
        budget -= len(tastes) + 2
    full = note
    note = _truncate_content(full, SELF_NOTE_FILENAME, max_chars=budget, read_path=str(path))
    # The marker lists the omitted headings, so the head/tail split does not cover it: shrink the split by the
    # overshoot until head, marker and tail fit together, so the tail is kept rather than sliced off.
    split = budget
    while len(note) > budget and split > 0:
        split = max(split - (len(note) - budget), 0)
        note = _truncate_content(full, SELF_NOTE_FILENAME, max_chars=split, read_path=str(path), queue_warning=False)
    # Headings are unbounded, so a marker alone can still exceed the section: the cap is the last resort.
    content = "\n\n".join(part for part in (SELF_NOTE_BOUNDARY, tastes, note) if part)
    content = content[:MAX_SYSTEM_PROMPT_SECTION_CHARS]
    return (RenderedPluginSystemPromptSection(
        id=SELF_NOTE_SECTION_ID, content=content, position="after_memory", plugin="core"),)
