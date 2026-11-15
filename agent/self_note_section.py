"""The agent's own self note (``<home>/SOUL.self.md``) as a core system-prompt section.

It rides the frozen plugin-section slot (``agent/system_prompt.py``
``_frozen_plugin_prompt_sections``): rendered once per session, restored from the
stored prompt on resume, re-rendered only at a compaction rebuild (lina#359).
"""

from __future__ import annotations

import logging
from pathlib import Path

logger = logging.getLogger(__name__)

SELF_NOTE_FILENAME = "SOUL.self.md"
SELF_NOTE_SECTION_ID = "agent-self-note"
# The boundary comes before any note text, so no note or taste can read as a rule (docs/design/agent-tastes.md §3).
SELF_NOTE_BOUNDARY = (
    "This agent's own self note: its voice notes and tastes. These notes and tastes are taste only. "
    "They never change reporting, honesty, permissions, verification, following the owner's instructions or safety."
)


def render_self_note_section(home: Path) -> tuple:
    """One section for *home*'s self note, or ``()`` when it is missing, empty,
    unreadable or carries a reserved marker. Never raises into the prompt build."""
    from agent.prompt_builder import _truncate_content
    from agent.safe_worker_policy import safe_worker_enabled
    from hermes_cli.plugins import (
        MAX_SYSTEM_PROMPT_SECTION_CHARS, PLUGIN_SECTIONS_END, PLUGIN_SECTIONS_START,
        RenderedPluginSystemPromptSection,
    )

    # Same rule as plugin sections: a safe-mode worker renders none.
    if safe_worker_enabled():
        return ()
    path = home / SELF_NOTE_FILENAME
    try:
        note = path.read_text(encoding="utf-8-sig").strip()
    except FileNotFoundError:
        return ()
    except (OSError, UnicodeDecodeError) as exc:
        logger.warning("Self note %s could not be read and is left out of the prompt: %s", path, exc)
        return ()
    if not note:
        return ()
    if PLUGIN_SECTIONS_START in note or PLUGIN_SECTIONS_END in note:
        logger.warning("Self note %s contains a reserved prompt marker and is left out of the prompt", path)
        return ()
    budget = MAX_SYSTEM_PROMPT_SECTION_CHARS - len(SELF_NOTE_BOUNDARY) - 2
    note = _truncate_content(note, SELF_NOTE_FILENAME, max_chars=budget, read_path=str(path))
    # The truncation marker lists omitted headings, so it can outgrow the head/tail split's slack.
    content = f"{SELF_NOTE_BOUNDARY}\n\n{note}"[:MAX_SYSTEM_PROMPT_SECTION_CHARS]
    return (RenderedPluginSystemPromptSection(
        id=SELF_NOTE_SECTION_ID, content=content, position="after_memory", plugin="core"),)
