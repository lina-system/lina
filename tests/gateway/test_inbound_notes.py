"""lina#324 (absorbing #310): every note the gateway adds to an inbound message is kept as a part of its own, never
inside the owner's words, while the model still reads the same bytes in the same order.

One Discord message in a shared room carries every note the gateway writes: the sender label and the room's channel
context, an image reading, an audio and a video note, a document note, a reply quote of the agent's own message and
the triggering-message note. The text runs through the real ``_prepare_inbound_message_text`` and the persist step
``_hmwa_apply_message_timestamp``.
"""

from __future__ import annotations

import json
from types import SimpleNamespace

import pytest

from agent.session_persistence import _db_flush_row
from agent.turn_context import substitute_api_content
from gateway import inbound_notes
from gateway.config import GatewayConfig, Platform
from gateway.platforms.event import MessageEvent, MessageType
from gateway.run import GatewayRunner
from gateway.session import SessionSource

OWNER = "Which of these should I keep?"
SCREEN = "A desk with two lamps and a stack of letters"
QUOTED = "I sorted the letters by date."
# The text the model reads for this message, exactly as the gateway composed it before notes became parts.
COMPOSED = (
    "[Triggering message id: `9001` — use as `message_id` for reply/react/pin via the discord tools.]\n\n"
    "[Replying to your previous message: \"I sorted the letters by date.\"]\n\n"
    "[The user sent a document: 'brief.pdf'. It is saved at: /cache/brief.pdf. Its text is not inlined "
    "here (it's a binary format such as PDF or DOCX). To read it, extract the document's text yourself — "
    "for example with the terminal tool or the ocr-and-documents skill — before answering, instead of "
    "asking the user to paste the contents.]\n\n"
    "[The user sent a video attachment: 'movie.mp4'. It is saved at: /cache/movie.mp4. Its content is "
    "not inlined here. If the user's request involves what the video contains, inspect or process it "
    "yourself — for example by passing the path to a video analysis or media tool — instead of asking "
    "the user to describe it. Only ask what to do with it if their intent is genuinely unclear.]\n\n"
    "[The user sent an audio file attachment: 'clip.mp3'. It is saved at: /cache/clip.mp3. Its content "
    "is not inlined here. If the user's request involves what the audio contains, transcribe or process "
    "it yourself — for example by passing the path to a transcription or media tool — instead of asking "
    "the user to describe it. Only ask what to do with it if their intent is genuinely unclear.]\n\n"
    "[The user sent an image~ Here's what I can see:\nA desk with two lamps and a stack of letters]\n[If "
    "you need a closer look, use vision_analyze with image_url: /cache/photo.png ~]\n\n"
    "[Earlier in the room]\n\n"
    "[New message]\n[Rin] Which of these should I keep?"
)


@pytest.fixture
def prepared(monkeypatch):
    async def vision(image_url, user_prompt):
        return json.dumps({"success": True, "analysis": SCREEN})

    monkeypatch.setattr("gateway.session._discord_tools_loaded", lambda: True)
    monkeypatch.setattr("tools.vision_tools.vision_analyze_tool", vision)
    runner = object.__new__(GatewayRunner)
    runner.config = GatewayConfig(group_sessions_per_user=False)
    runner.adapters = {}
    runner._pending_native_image_paths_by_session = {}
    runner._decide_image_input_mode = lambda **_: "text"
    runner._resolve_session_agent_runtime = lambda **_: ("test-model", {})
    source = SessionSource(platform=Platform.DISCORD, chat_id="room-1", chat_type="group", user_id="u-7",
                           user_name="Rin")
    event = MessageEvent(
        text=OWNER, source=source, message_id="9001", message_type=MessageType.DOCUMENT,
        media_urls=["/cache/photo.png", "/cache/clip.mp3", "/cache/movie.mp4", "/cache/brief.pdf"],
        media_types=["image/png", "audio/mpeg", "video/mp4", "application/pdf"],
        media_filenames=["photo.png", "clip.mp3", "movie.mp4", "brief.pdf"],
        reply_to_message_id="9000", reply_to_text=QUOTED,
    )
    event.reply_to_is_own_message = True
    event.channel_context = "[Earlier in the room]"
    return runner, event, source


async def _compose(runner, event, source):
    text = await runner._prepare_inbound_message_text(event=event, source=source, history=[])
    return runner._hmwa_apply_message_timestamp(event, text)


@pytest.mark.asyncio
async def test_every_byte_the_model_reads_is_a_recorded_note_or_the_owner_words(prepared):
    model_text, persisted, _ = await _compose(*prepared)
    notes = inbound_notes.of(prepared[1])

    # A note added to the text without being recorded makes the two differ, and the turn's notes stay in its text.
    assert notes.compose() == model_text
    assert persisted == OWNER
    stored = inbound_notes.display_metadata(prepared[1])[inbound_notes.GATEWAY_NOTES_KEY]
    assert stored["owner_text"] == OWNER
    assert [part["kind"] for part in stored["parts"]] == [
        "sender_label", "channel_context", "image_reading", "audio_note", "video_note", "document_note",
        "reply_quote", "trigger_note"]
    assert {part["kind"] for part in stored["parts"]} <= inbound_notes.KINDS
    assert not any(OWNER in part["text"] for part in stored["parts"])
    assert [(part["kind"], part["quoted_speaker"]) for part in stored["parts"] if part["quoted_speaker"]] == [
        ("channel_context", "outside"), ("image_reading", "agent"), ("reply_quote", "agent")]


@pytest.mark.asyncio
async def test_the_request_bytes_are_unchanged_and_replay_the_same(prepared):
    model_text, persisted, _ = await _compose(*prepared)
    agent = SimpleNamespace(_persist_user_message_override=persisted, _persist_user_message_timestamp=None)

    row = _db_flush_row(agent, {"role": "user", "content": model_text}, True)
    replayed = {"role": "user", "content": row["content"], "api_content": row["api_content"]}
    substitute_api_content(replayed)

    assert model_text == COMPOSED
    assert row["content"] == OWNER
    assert replayed["content"] == COMPOSED


@pytest.mark.asyncio
async def test_a_step_that_rewrites_the_text_keeps_the_notes_in_it(prepared):
    runner, event, source = prepared
    event.media_urls = event.media_types = event.media_filenames = []
    text = await runner._prepare_inbound_message_text(event=event, source=source, history=[])

    notes = inbound_notes.of(event)
    notes.step("voice_note", text, "[voice message could not be transcribed]")

    assert inbound_notes.persist_text(event, "[voice message could not be transcribed]") == (
        "[voice message could not be transcribed]")
    assert inbound_notes.display_metadata(event) == {}
