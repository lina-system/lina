#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = []
# ///
# Run with python scripts/check_report_links.py REGISTRY_DIR BASE_URL.
"""Verify the old registry's /r/ links against a trusted dashboard origin."""

from __future__ import annotations

import argparse
import http.client
import ipaddress
import json
import os
import re
from contextlib import closing
from html.parser import HTMLParser
from pathlib import Path
from typing import Final
from urllib.parse import urlsplit

SESSION_ENV: Final = "HERMES_DASHBOARD_SESSION_TOKEN"
SESSION_HEADER: Final = "X-Hermes-Session-Token"
SLUG: Final = re.compile(r"[a-z0-9][a-z0-9_-]*")


class ReportTitle(HTMLParser):
    """Capture the first complete title, including decoded character references."""

    def __init__(self) -> None:
        super().__init__()
        self.title: str | None = None
        self._parts: list[str] | None = None

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        if tag == "title" and self.title is None and self._parts is None:
            self._parts = []

    def handle_data(self, data: str) -> None:
        if self._parts is not None:
            self._parts.append(data)

    def handle_endtag(self, tag: str) -> None:
        if tag == "title" and self._parts is not None:
            self.title = "".join(self._parts)
            self._parts = None


def dashboard_connection(base_url: str) -> http.client.HTTPConnection | None:
    """Accept an origin with verified HTTPS or local cleartext HTTP."""
    if re.search(r"[\s\x00-\x1f\x7f?#\\]", base_url):
        return None
    try:
        url = urlsplit(base_url)
        port = url.port
    except ValueError:
        return None
    host = url.hostname
    if not host or url.username is not None or url.path not in ("", "/") or port == 0:
        return None
    if url.scheme == "https":
        return http.client.HTTPSConnection(host, port, timeout=10)
    if url.scheme == "http":
        try:
            loopback = ipaddress.ip_address(host).is_loopback
        except ValueError:
            loopback = host == "localhost"
        if loopback:
            return http.client.HTTPConnection(host, port, timeout=10)
    return None


def check_link(
    connection: http.client.HTTPConnection,
    path: str,
    expected_title: str | None,
    headers: dict[str, str],
) -> tuple[int | str, str]:
    # An HTTP connection sends a single GET directly, without proxy or redirect handling.
    try:
        with closing(connection):
            connection.request("GET", path, headers=headers)
            with connection.getresponse() as response:
                if response.status != 200 or expected_title is None:
                    return response.status, "n/a"
                title = ReportTitle()
                charset = response.headers.get_content_charset() or "utf-8"
                try:
                    title.feed(response.read().decode(charset))
                    title.close()
                except (LookupError, UnicodeError):
                    return response.status, "no"
                return response.status, "yes" if title.title == expected_title else "no"
    except (OSError, http.client.HTTPException, ValueError):
        return "request-error", "n/a"


def setup_failure(status: str) -> int:
    print(f"- status={status} title-match=n/a")
    print("missing=0 fail=1")
    return 2


def main() -> int:
    parser = argparse.ArgumentParser(
        description=__doc__,
        epilog=f"Set {SESSION_ENV} in the environment for authenticated requests.",
        allow_abbrev=False,
    )
    parser.add_argument("registry", type=Path, metavar="REGISTRY_DIR")
    parser.add_argument("base_url", metavar="BASE_URL")
    # Avoid echoing unexpected arguments, which may contain a session token.
    args, extras = parser.parse_known_args()
    if extras:
        return setup_failure("invalid-arguments")

    connection = dashboard_connection(args.base_url)
    if connection is None:
        return setup_failure("invalid-base-url")
    token = os.environ.get(SESSION_ENV)
    if token and not re.fullmatch(r"[!-~]+", token):
        return setup_failure("invalid-session-token")
    headers = {SESSION_HEADER: token} if token else {}

    try:
        index = json.loads((args.registry / "index.json").read_bytes())
    except (OSError, ValueError):
        return setup_failure("invalid-index")
    if not isinstance(index, dict) or not isinstance(index.get("reports"), list):
        return setup_failure("invalid-index")

    missing = fail = 0
    for row in index["reports"]:
        slug = row.get("slug") if isinstance(row, dict) else None
        if not isinstance(slug, str) or not SLUG.fullmatch(slug):
            print("- status=invalid-slug title-match=n/a")
            fail += 1
            continue
        path = f"/r/{slug}"
        gone = row.get("gone", False)
        title = row.get("title")
        if not isinstance(gone, bool) or (not gone and not isinstance(title, str)):
            print(f"{path} status=invalid-row title-match=n/a")
            fail += 1
            continue

        status, match = check_link(connection, path, None if gone else title, headers)
        print(f"{path} status={status} title-match={match}")
        if not gone and status == 404:
            missing += 1
        elif (gone and status != 404) or (not gone and match != "yes"):
            fail += 1

    print(f"missing={missing} fail={fail}")
    return int(bool(missing or fail))


if __name__ == "__main__":
    raise SystemExit(main())
