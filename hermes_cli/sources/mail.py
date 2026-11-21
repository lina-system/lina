"""Fetch inbox IDs and read each message without downloading attachments."""

from __future__ import annotations

import base64
import hashlib
import math
from datetime import UTC, datetime
from email.utils import parsedate_to_datetime, parseaddr
from html.parser import HTMLParser
from urllib.parse import quote

from .adapters import TEXT_LIMIT, SourceError, Window, display_link, iso, paged_rows, payload, rows
from .defaults import MAIL_QUERY
from .spool import message_uid

LIST = "GMAIL_FETCH_EMAILS"
DETAIL = "GMAIL_FETCH_MESSAGE_BY_MESSAGE_ID"


class _PlainHTML(HTMLParser):
    def __init__(self):
        super().__init__()
        self.hidden = 0
        self.parts = []

    def handle_starttag(self, tag, attrs):
        if tag in {"style", "script", "head"}:
            self.hidden += 1
        if tag in {"p", "br", "div", "li", "tr"}:
            self.parts.append("\n")

    def handle_endtag(self, tag):
        if tag in {"style", "script", "head"} and self.hidden:
            self.hidden -= 1

    def handle_data(self, data):
        if not self.hidden:
            self.parts.append(data)


def _plain(text):
    parser = _PlainHTML()
    parser.feed(text)
    parser.close()
    return "".join(parser.parts).strip()


def _mime(part, plain, html, names):
    if not isinstance(part, dict):
        raise SourceError("invalid_response")
    name = part.get("filename")
    if isinstance(name, str) and name:
        names.append(name)
        return
    mime = part.get("mimeType", "")
    if mime in {"text/plain", "text/html"}:
        data = (part.get("body") or {}).get("data")
        if isinstance(data, str) and data:
            try:
                chunk = "".join(data.split())[: TEXT_LIMIT * 8]
                text = base64.urlsafe_b64decode(chunk + "=" * (-len(chunk) % 4)).decode("utf-8", "replace")
            except ValueError:
                raise SourceError("invalid_response") from None
            (plain if mime == "text/plain" else html).append(text)
    for child in part.get("parts") or []:
        _mime(child, plain, html, names)


def _timestamp(item, headers):
    raw = item.get("internalDate") or item.get("messageTimestamp") or item.get("timestamp") or headers.get("date")
    try:
        if isinstance(raw, (float, int)) or (isinstance(raw, str) and raw.isdigit()):
            seconds = float(raw)
            return datetime.fromtimestamp(seconds / 1000 if seconds > 10**11 else seconds, UTC)
        try:
            stamp = datetime.fromisoformat(raw)
        except ValueError:
            stamp = parsedate_to_datetime(raw)
        if stamp.utcoffset() is not None:
            return stamp
    except (AttributeError, TypeError, ValueError, OverflowError, OSError):
        pass
    raise SourceError("invalid_response")


def _message(item, native, account):
    if (item.get("messageId") or item.get("id")) != native:
        raise SourceError("invalid_response")
    part = item.get("payload") or {}
    headers = {h["name"].lower(): h["value"] for h in part.get("headers") or []
               if isinstance(h, dict) and isinstance(h.get("name"), str) and isinstance(h.get("value"), str)}
    stamp = _timestamp(item, headers)
    name, address = parseaddr(item.get("sender") or headers.get("from", ""))
    subject = item.get("subject") or headers.get("subject", "")
    plain, html, names = [], [], []
    _mime(part, plain, html, names)
    for attachment in item.get("attachmentList") or item.get("attachments") or []:
        if isinstance(attachment, dict):
            filename = attachment.get("filename") or attachment.get("fileName")
            if isinstance(filename, str) and filename:
                names.append(filename)
    body = "\n".join(plain) or item.get("messageText") or "\n".join(html) or item.get("snippet") or ""
    if not plain:
        body = _plain(body)
    thread = item.get("threadId") or native
    room = f"{account}:{thread}"
    uid = message_uid("mail", room, native)
    url = display_link(item.get("display_url") or item.get("displayUrl"))
    if not url:
        url = f"https://mail.google.com/mail/u/?authuser={quote(account, safe='')}#all/{quote(native, safe='')}"
    return stamp, {
        "source": "mail", "room_id": room, "room_name": subject, "account": account,
        "native_id": native, "uid": uid, "ts": iso(stamp), "ingest_path": "live",
        "author_id": address, "author_name": name or address,
        "text": "\n".join(filter(None, [subject, body])).strip()[:TEXT_LIMIT],
        "attachments": [{"name": n} for n in dict.fromkeys(names)],
        "thread_root_uid": message_uid("mail", room, thread), "reply_to_uid": None,
        "display_url": url,
    }


def collect(config, since, until, rooms, call, emit, checkpoint, *, probe=False):
    accounts = config.get("accounts", [])
    query = config.get("query", MAIL_QUERY)
    if not isinstance(query, str) or not query.strip() or not accounts:
        return
    if not isinstance(accounts, list) or any(not isinstance(a, str) or not a.strip() for a in accounts):
        raise SourceError("invalid_config")

    def priority(account):
        prior = rooms.get(account, {})
        return (not bool(prior.get("pending")), prior.get("cursor", ""))

    for account in sorted(dict.fromkeys(accounts), key=priority):
        if account.startswith("-"):
            raise SourceError("invalid_config")
        query_hash = hashlib.sha256(query.encode()).hexdigest()
        if rooms.get(account, {}).get("pending", {}).get("query_hash", query_hash) != query_hash:
            rooms = {k: v for k, v in rooms.items() if k != account}
        window = Window(rooms, account, since, until, checkpoint)
        start, end = window.start, window.end
        window.position["query_hash"] = query_hash
        # Pin the two-day default to this window. Re-evaluating newer_than on retry loses old unread IDs.
        bounded_query = query.removeprefix("newer_than:2d ") if query == MAIL_QUERY else query
        params = {"user_id": "me", "ids_only": True, "max_results": 1 if probe else 100,
                  "include_spam_trash": False,
                  "query": (f"{bounded_query} after:{math.floor(start.timestamp())} "
                            f"before:{math.ceil(end.timestamp())}")}
        if probe:
            rows(payload(call(LIST, params, account)), "messages")
            return
        if start >= end:
            continue
        window.save()
        for ref in paged_rows(call, LIST, params, account, window, "messages",
                              lambda item: item.get("messageId") or item.get("id"), gmail=True):
            native = ref.get("messageId") or ref["id"]
            data = payload(call(DETAIL, {"user_id": "me", "message_id": native, "format": "full"}, account))
            stamp, message = _message(data, native, account)
            if start <= stamp < end:
                emit(message)
        window.finish()
