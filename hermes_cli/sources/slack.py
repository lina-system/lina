"""Read the authenticated user's conversations and their visible threads."""

from __future__ import annotations

import html
import re
from datetime import UTC, datetime, timedelta

from .adapters import TEXT_LIMIT, SourceError, Window, display_link, iso, paged_rows, payload, rows
from .spool import message_uid

LIST = "SLACK_LIST_ALL_CHANNELS"
HISTORY = "SLACK_FETCH_CONVERSATION_HISTORY"
REPLIES = "SLACK_FETCH_MESSAGE_THREAD_FROM_A_CONVERSATION"
PERMALINK = "SLACK_RETRIEVE_MESSAGE_PERMALINK_URL"
_EPOCH = datetime(1970, 1, 1, tzinfo=UTC)


def _stamp(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{1,12}(?:\.[0-9]{1,6})?", value):
        raise SourceError("invalid_response")
    seconds, _, fraction = value.partition(".")
    try:
        return _EPOCH + timedelta(seconds=int(seconds), microseconds=int(fraction.ljust(6, "0")))
    except OverflowError:
        raise SourceError("invalid_response") from None


def _ts(stamp):
    delta = stamp - _EPOCH
    return f"{delta.days * 86400 + delta.seconds}.{delta.microseconds:06d}"


def _emit(room, item, root, start, until, call, emit):
    native = item.get("ts")
    stamp = _stamp(native)
    if not start <= stamp < until:
        return
    channel = room["id"]
    link = item.get("permalink")
    if not link:
        data = payload(call(PERMALINK, {"channel": channel, "message_ts": native}, None))
        link = data.get("permalink")
    link = display_link(link)
    if not link:
        raise SourceError("invalid_response")
    author = item.get("user") or item.get("bot_id") or ""
    profile = item.get("user_profile") or item.get("bot_profile") or {}
    edited = (item.get("edited") or {}).get("ts")
    root_id = item.get("thread_ts") or root
    root_uid = message_uid("slack", channel, root_id) if root_id else None
    text = item.get("text") or ""
    text = html.unescape(re.sub(r"<([^<>|]+)(?:\|([^<>]+))?>", lambda m: m[2] or m[1], text))
    emit({
        "source": "slack", "room_id": channel, "room_name": room.get("name") or channel,
        "account": room.get("context_team_id") or room.get("team") or "",
        "native_id": native, "ts": iso(stamp), "uid": message_uid("slack", channel, native),
        "ingest_path": "live", "author_id": author,
        "author_name": profile.get("display_name") or profile.get("real_name") or item.get("username") or author,
        "text": text[:TEXT_LIMIT], "permalink": link, "edited_ts": iso(_stamp(edited)) if edited else None,
        "thread_root_uid": root_uid, "reply_to_uid": root_uid if root_id and native != root_id else None,
        "attachments": [{"name": f["name"]} for f in item.get("files") or []
                        if isinstance(f, dict) and isinstance(f.get("name"), str) and f["name"]],
    })


def _room(room, window, call, emit):
    start, until = window.start, window.end
    position = window.position
    params = {"channel": room["id"], "oldest": _ts(start), "latest": _ts(until),
              "inclusive": True, "limit": 100}
    for item in paged_rows(call, HISTORY, params, None, window, "messages", lambda row: row.get("ts")):
        native = item["ts"]
        replies = item.get("reply_count") or 0
        if type(replies) is not int or replies < 0:
            raise SourceError("invalid_response")
        if position.get("thread_ts") != native:
            _emit(room, item, native if replies else None, start, until, call, emit)
        if not replies or item.get("thread_ts", native) != native:
            continue
        if position.get("thread_ts") != native:
            position.update(thread_ts=native, thread_offset=0, thread_page_token=None)
            window.save()
        for reply in paged_rows(call, REPLIES, {**params, "ts": native}, None, window, "messages",
                                lambda row: row.get("ts"), thread=True):
            if reply["ts"] != native:
                _emit(room, reply, native, start, until, call, emit)
        for key in ("thread_ts", "thread_page_token", "thread_offset", "thread_page_hash"):
            position.pop(key, None)


def collect(config, since, until, rooms, call, emit, checkpoint, *, probe=False):
    params = {"types": "public_channel,private_channel,im,mpim", "exclude_archived": True,
              "limit": 1 if probe else 100}
    if probe:
        rows(payload(call(LIST, params, None)), "channels")
        return
    listing = Window(rooms, "@channels", since, until, checkpoint)
    listing.save()
    for room in paged_rows(call, LIST, params, None, listing, "channels", lambda row: row.get("id")):
        if room.get("is_archived") or not any(room.get(key) is True for key in ("is_member", "is_im", "is_mpim")):
            continue
        window = Window(rooms, room["id"], listing.start, listing.end, checkpoint)
        if window.start < window.end:
            window.save()
            _room(room, window, call, emit)
            window.finish()
    pending = getattr(checkpoint, "pending", None)
    if pending:
        pending("@channels", None)
