"""The two manual producers share the existing v1 ingest boundary."""

import json
import stat
from concurrent.futures import ThreadPoolExecutor

import pytest

from hermes_cli.external_messages import _event
from hermes_cli.sources.spool import append_message
from tests.hermes_cli.test_external_messages import message


def test_concurrent_appends_preserve_the_prefix_identity_and_links(tmp_path):
    path = tmp_path / "private" / "events.jsonl"
    rows = [message("mail", "box:thread", str(i), display_url="https://example.test/mail")
            if i % 2 else message("slack", "channel", str(i), permalink="https://example.test/slack")
            for i in range(24)]
    append_message(path, rows[0])
    prefix = path.read_bytes()
    with ThreadPoolExecutor(max_workers=6) as pool:
        list(pool.map(lambda msg: append_message(path, msg), rows[1:]))
    data = path.read_bytes()
    assert data.startswith(prefix)
    parsed = [_event(line, 0)[0] for line in data.splitlines()]
    assert sorted(row.uid for row in parsed) == sorted("messages:" + m["uid"] for m in rows)
    for row in parsed:
        native = json.loads(row.native)
        field = "display_url" if native["platform"] == "mail" else "permalink"
        assert native[field] == "https://example.test/" + native["platform"]
    assert stat.S_IMODE(path.stat().st_mode) == 0o600
    assert stat.S_IMODE(path.parent.stat().st_mode) == 0o700


def test_incomplete_spool_is_preserved_for_recovery(tmp_path):
    path = tmp_path / "events.jsonl"
    append_message(path, message("slack"))
    with path.open("ab") as stream:
        stream.write(b'{"version":')
    original = path.read_bytes()
    with pytest.raises(ValueError, match="incomplete"):
        append_message(path, message("mail"))
    assert path.read_bytes() == original
