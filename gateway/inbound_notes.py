"""The notes the gateway adds to an inbound message, kept apart from the owner's words (lina#324, absorbing #310).

``_prepare_inbound_message_text`` builds the text the model reads by putting notes in front of what the person wrote:
an image reading, a voice, audio, video or document note, a reply quote, Discord's triggering-message note, the
sender label and channel context of a shared room. Each step reports the text before and after it here, and the
exact bytes it put in front are kept as a note of its kind. The model still reads the composed text (it is the user
row's ``api_content``, so a replay sends the same bytes); the stored row is the owner's words alone, and each note is
a part of its own beside it (``memory.message_parts``, written by ``hermes_state_store.put_row_text``).
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any, Optional

from hermes_state_store import GATEWAY_NOTES_KEY

logger = logging.getLogger("gateway.run")

KINDS = frozenset({"reply_quote", "image_reading", "voice_note", "audio_note", "video_note", "document_note",
                   "trigger_note", "sender_label", "channel_context"})
_ATTR = "_gateway_inbound_notes"


@dataclass(frozen=True)
class Note:
    kind: str
    prefix: str  # the exact bytes the step put in front, separator included
    quoted_speaker: Optional[str] = None  # whose words the note quotes or reads out
    quoted_ref: Optional[str] = None


@dataclass
class InboundNotes:
    owner_text: str
    parts: list[Note] = field(default_factory=list)  # in the order the steps added them, innermost first
    intact: bool = True
    kept: bool = False  # the user row stores the owner's words and these notes as parts (persist_text)

    def step(self, kind: str, before: str, after: str, *, quoted_speaker: Optional[str] = None,
             quoted_ref: Optional[str] = None) -> str:
        """Record the note of *kind* one step put in front of the text, and return *after*."""
        if kind not in KINDS:
            raise ValueError(f"unknown gateway note kind {kind!r}")
        if after != before and self.intact:
            if after.endswith(before):
                self.parts.append(Note(kind, after[:len(after) - len(before)], quoted_speaker, quoted_ref))
            else:
                self._rewritten(kind)
        return after

    def expanded(self, before: str, after: str) -> str:
        """Record text appended after the message (an ``@`` reference's attached context): it is the message's own,
        as a typed turn stores it. Returns *after*."""
        if after != before and self.intact:
            if after.startswith(before):
                self.owner_text += after[len(before):]
            else:
                self._rewritten("the reference expansion")
        return after

    def _rewritten(self, step: str) -> None:
        # A step that rewrote the text (Discord's empty-content placeholder dropped under a voice note) leaves no
        # owner's words to tell apart: the turn is stored as composed, the way it was before notes were parts.
        logger.warning("gateway notes: %s rewrote the inbound text; this turn's notes stay in its text", step)
        self.intact = False

    def compose(self) -> str:
        """The text the model reads: every note's bytes, outermost first, then the owner's words."""
        return "".join(note.prefix for note in reversed(self.parts)) + self.owner_text

    def metadata(self) -> dict[str, Any]:
        if not self.kept:
            return {}
        return {GATEWAY_NOTES_KEY: {"owner_text": self.owner_text, "parts": [
            {"kind": note.kind, "text": note.prefix.strip(), "quoted_speaker": note.quoted_speaker,
             "quoted_ref": note.quoted_ref} for note in self.parts]}}


def start(event: Any, text: str) -> InboundNotes:
    notes = InboundNotes(text)
    setattr(event, _ATTR, notes)
    return notes


def of(event: Any) -> Optional[InboundNotes]:
    notes = getattr(event, _ATTR, None)
    return notes if isinstance(notes, InboundNotes) else None


def persist_text(event: Any, composed: Any) -> Any:
    """The text the user row stores for *event*: the owner's words when every byte of *composed* (the text the model
    reads) is a recorded note or the owner's words, else *composed* itself."""
    notes = of(event)
    if notes is None or not (notes.intact and notes.parts) or not isinstance(composed, str):
        return composed
    if notes.compose() != composed:
        logger.warning("gateway notes: the inbound text changed after its notes were recorded; they stay in its text")
        notes.intact = False
        return composed
    notes.kept = True
    return notes.owner_text


def display_metadata(event: Any) -> dict[str, Any]:
    notes = of(event)
    return notes.metadata() if notes is not None else {}
