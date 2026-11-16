"""A chat message's reference to a report (lina#263, ``docs/api/work-client.yaml`` "ReportReference").

The ``report`` tool returns the reference line and the agent sends it as the message, after nothing longer than
the one-line summary it already holds. The line is ``[[report:<id>|<page address>|<summary>]]`` on a line of its
own. A client with cards (the app, lina-app#50) shows a report card for it and reads the report from the client
API; a platform without cards sends :func:`plain` instead: the summary line, then the page link. The report's body
never travels in the chat.
"""

from __future__ import annotations

import re
from typing import NamedTuple

LINE = re.compile(r"^\[\[report:(rep_[0-9a-f]{12})\|([^|\s]+)\|(.+?)\]\][ \t]*$", re.MULTILINE)


class Reference(NamedTuple):
    report_id: str
    page: str
    summary: str


def line(report_id: str, page: str, summary: str) -> str:
    return f"[[report:{report_id}|{page}|{' '.join(summary.split())}]]"


def references(text: str) -> list[Reference]:
    return [Reference(*match.groups()) for match in LINE.finditer(text or "")]


def plain(text: str) -> str:
    """*text* with every reference line as its summary line and the page link below it."""
    if not text or "[[report:" not in text:
        return text
    return LINE.sub(lambda match: f"{match.group(3)}\n{match.group(2)}", text)
