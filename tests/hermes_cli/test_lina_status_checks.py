"""``GET /api/lina/status/checks`` (lina-system/lina#233): the hub's own health checks, served from
``<LINA root>/status/*.json``.

The checks run outside LINA's code, so the fixture is the folder they write: one small status file per check, a
malformed one, and no folder at all. The served JSON is validated against ``docs/api/board-client.yaml`` with the
helper pattern of ``test_lina_board_api.py``.
"""

from __future__ import annotations

import json
import os
import time
from pathlib import Path

import jsonschema
import pytest
from fastapi.testclient import TestClient

import hermes_yaml
from hermes_cli import lina_ocx

REPO = Path(__file__).resolve().parents[2]

def _openapi_to_jsonschema(node):
    """OpenAPI 3.0 ``nullable: true`` -> JSON Schema ``type: [t, "null"]``."""
    if isinstance(node, list):
        return [_openapi_to_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _openapi_to_jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    return out

_COMPONENTS = _openapi_to_jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/board-client.yaml").read_text(encoding="utf-8"))["components"])

def _valid(data):
    jsonschema.Draft7Validator(
        {"$ref": "#/components/schemas/StatusChecks", "components": _COMPONENTS}).validate(data)
    return data

@pytest.fixture
def root(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    (root / "status").mkdir(parents=True)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    return root

@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client

def _file(root, name, *, at, ok=True, line="all good", stale_after=3600, check=None) -> Path:
    """One check's status file; *check* overrides the check's own name (the file stem otherwise)."""
    data = {"name": check or Path(name).stem, "ok": ok, "line": line, "at": at, "stale_after": stale_after}
    path = root / "status" / name
    path.write_text(json.dumps(data), encoding="utf-8")
    return path

def _checks(client) -> dict:
    response = client.get("/api/lina/status/checks")
    assert response.status_code == 200
    return _valid(response.json())

def _strict(text: str) -> dict:
    """Parse a body the way a browser's JSON.parse does: a bare ``NaN``/``Infinity`` token is a syntax error."""
    return json.loads(text, parse_constant=lambda token: (_ for _ in ()).throw(ValueError(token)))

def _raw(at, stale_after) -> str:
    """The status file's text with *at* and *stale_after* as literal tokens, for the values JSON has no number for."""
    return f'{{"name":"t","ok":true,"line":"x","at":{at},"stale_after":{stale_after}}}'

def test_ok_failing_and_stale_rows_carry_their_state(root, client):
    now = time.time()
    _file(root, "tools.json", check="tools", line="3 machines in step", at=now - 30)
    _file(root, "backup.json", check="backup", ok=False, line="backup line down", at=now - 20)
    _file(root, "standby.json", check="standby", line="last seen an hour ago", at=now - 7200)

    body = _checks(client)

    assert body["version"] == 1
    assert body["at"] == pytest.approx(now, abs=60)
    rows = {row["file"]: row for row in body["checks"]}
    assert (rows["tools.json"]["state"], rows["tools.json"]["stale"], rows["tools.json"]["ok"]) == ("ok", False, True)
    assert (rows["backup.json"]["state"], rows["backup.json"]["stale"]) == ("failing", False)
    assert (rows["standby.json"]["state"], rows["standby.json"]["stale"]) == ("stale", True)
    assert rows["standby.json"]["staleAfter"] == 3600
    assert rows["standby.json"]["line"] == "last seen an hour ago"
    assert rows["tools.json"]["at"] == pytest.approx(now - 30)
    assert rows["standby.json"]["at"] == pytest.approx(now - 7200)

def test_stale_wins_over_failing(root, client):
    _file(root, "backup.json", ok=False, line="backup line down", at=time.time() - 7200, stale_after=60)

    row = _checks(client)["checks"][0]

    assert (row["name"], row["ok"], row["state"], row["stale"]) == ("backup", False, "stale", True)

def test_a_malformed_file_is_one_error_row_naming_it(root, client):
    now = time.time()
    _file(root, "tools.json", at=now - 30)
    broken = root / "status" / "broken.json"
    broken.write_text("{not json", encoding="utf-8")

    body = _checks(client)

    assert len(body["checks"]) == 2
    row = next(row for row in body["checks"] if row["file"] == "broken.json")
    assert (row["name"], row["ok"], row["state"], row["stale"], row["staleAfter"]) == ("broken", False, "error",
                                                                                        False, None)
    assert row["line"].startswith("broken.json: ")
    assert row["at"] == pytest.approx(broken.stat().st_mtime)
    assert next(row for row in body["checks"] if row["file"] == "tools.json")["state"] == "ok"

MISSING = object()

@pytest.mark.parametrize("field, value, line", [
    ("stale_after", MISSING, "missing stale_after"),
    ("name", 7, "name is not a string"),
    ("ok", 1, "ok is not a boolean"),
    ("line", 3, "line is not a string"),
    ("at", "soon", "at is not a number"),
])
def test_a_missing_or_wrong_typed_field_is_an_error_row(root, client, field, value, line):
    data = {"name": "tools", "ok": True, "line": "all good", "at": time.time() - 30, "stale_after": 3600}
    if value is MISSING:
        del data[field]
    else:
        data[field] = value
    (root / "status" / "tools.json").write_text(json.dumps(data), encoding="utf-8")

    row = _checks(client)["checks"][0]

    assert (row["state"], row["ok"], row["staleAfter"]) == ("error", False, None)
    assert row["line"] == f"tools.json: {line}"

HUGE = "1" * 400  # an integer literal too large for a float

@pytest.mark.parametrize("at, stale_after, line", [
    ("NaN", "60", "at is not a finite number"),
    ("Infinity", "60", "at is not a finite number"),
    (HUGE, "60", "at is not a finite number"),
    ("1800000000", "NaN", "stale_after is not a finite number"),
    ("1800000000", "-1", "stale_after is negative"),
])
def test_a_non_finite_or_negative_check_time_is_an_error_row_in_a_parseable_body(root, client, at, stale_after, line):
    """json.loads accepts NaN/Infinity and json.dumps writes them back as bare tokens a browser cannot parse, so one
    such file must not reach the response: it is an error row and every other check is still served."""
    (root / "status" / "tools.json").write_text(_raw(at, stale_after), encoding="utf-8")
    _file(root, "backup.json", at=time.time() - 30)

    response = client.get("/api/lina/status/checks")

    assert response.status_code == 200
    body = _valid(_strict(response.text))
    assert len(body["checks"]) == 2
    row = next(row for row in body["checks"] if row["file"] == "tools.json")
    assert (row["state"], row["ok"], row["stale"], row["staleAfter"]) == ("error", False, False, None)
    assert row["line"] == f"tools.json: {line}"
    assert next(row for row in body["checks"] if row["file"] == "backup.json")["state"] == "ok"

def test_a_directory_named_like_a_status_file_is_not_listed(root, client):
    (root / "status" / "dir.json").mkdir()
    _file(root, "tools.json", at=time.time() - 30)

    assert [row["file"] for row in _checks(client)["checks"]] == ["tools.json"]

SURROGATE = "\\ud800"  # a lone surrogate as a JSON escape: valid JSON, no UTF-8 body can carry it

@pytest.mark.parametrize("field", ["name", "line"])
def test_text_that_cannot_be_utf8_is_an_error_row_in_a_parseable_body(root, client, field):
    """A lone surrogate is valid JSON, but json.dumps(ensure_ascii=False) cannot encode it: one such file must not
    fail the whole response, so it is an error row and every other check is still served."""
    texts = {"name": '"t"', "line": '"x"'}
    texts[field] = f'"{SURROGATE}"'
    (root / "status" / "tools.json").write_text(
        f'{{"name":{texts["name"]},"ok":true,"line":{texts["line"]},"at":1800000000,"stale_after":60}}',
        encoding="utf-8")
    _file(root, "backup.json", at=time.time() - 30)

    response = client.get("/api/lina/status/checks")

    assert response.status_code == 200
    body = _valid(_strict(response.text))
    row = next(row for row in body["checks"] if row["file"] == "tools.json")
    assert (row["state"], row["ok"], row["stale"]) == ("error", False, False)
    assert row["line"] == f"tools.json: {field} is not valid text"
    assert next(row for row in body["checks"] if row["file"] == "backup.json")["state"] == "ok"

def test_a_file_name_that_is_not_utf8_is_served_with_replacement_characters():
    """On Linux a file name of raw bytes comes back from glob with surrogate escapes; the row must still encode."""
    from hermes_cli import lina_status_checks
    path = Path(os.fsdecode(b"bad\xff.json"))

    row = lina_status_checks._row(path, time.time())

    assert (row["file"], row["name"]) == ("bad\ufffd.json", "bad\ufffd")
    assert row["line"] == "bad\ufffd.json: unreadable"
    body = json.dumps(row, ensure_ascii=False)
    assert body.encode("utf-8") and "\udcff" not in body

def test_a_missing_folder_serves_an_empty_list(root, client):
    (root / "status").rmdir()

    body = _checks(client)

    assert body["checks"] == []

def test_checks_are_newest_first(root, client):
    now = time.time()
    for name, age in (("middle.json", 60), ("newest.json", 10), ("oldest.json", 600)):
        _file(root, name, at=now - age)

    assert [row["file"] for row in _checks(client)["checks"]] == ["newest.json", "middle.json", "oldest.json"]

def test_a_matching_etag_answers_304_until_a_check_changes(root, client):
    _file(root, "tools.json", at=time.time() - 30)
    first = client.get("/api/lina/status/checks")
    assert first.status_code == 200
    again = client.get("/api/lina/status/checks", headers={"If-None-Match": first.headers["etag"]})
    assert again.status_code == 304 and again.headers["etag"] == first.headers["etag"]

    _file(root, "backup.json", ok=False, at=time.time() - 10)
    changed = client.get("/api/lina/status/checks", headers={"If-None-Match": first.headers["etag"]})
    assert changed.status_code == 200 and changed.headers["etag"] != first.headers["etag"]
