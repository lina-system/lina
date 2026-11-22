"""Import the retired registry through the Desk CLI and open its original links."""

import hashlib
import json
from contextlib import closing

import pytest
from fastapi.testclient import TestClient

import hermes_state_reports as reports
from tests.hermes_cli.test_desk_reports import run

pytest_plugins = ["tests.hermes_cli.test_desk_reports"]


def record(directory, slug, *, gone=False):
    body = f"# Heading\n\nCopy for {slug}.\n"
    if not gone:
        (directory / f"{slug}.md").write_text(body, encoding="utf-8")
    return {"slug": slug, "rel": f"2_Areas/{slug}.md", "title": f"Published {slug}", "title_from": "given",
            "gone": gone, "sha": hashlib.sha256(body.encode()).hexdigest()[:16], "images": []}


@pytest.mark.platforms("posix")
def test_import_keeps_address_title_and_version_across_retries_and_sync(desk, tmp_path):
    from hermes_cli import web_server

    active = record(tmp_path, "original")
    gone = record(tmp_path, "withdrawn", gone=True)
    doc = desk[0] / active["rel"]
    snapshot = tmp_path / "original.md"
    body = snapshot.read_bytes() + b"\n![Graph](plot.png)\n"
    snapshot.write_bytes(body)
    doc.write_bytes(body)
    image = b"synthetic picture"
    (doc.parent / "plot.png").write_bytes(image)
    copies = tmp_path / "original.files"
    copies.mkdir()
    key = hashlib.sha256(b"plot.png").hexdigest()[:16]
    (copies / key).write_bytes(image)
    active.update(path=str(doc), sha=hashlib.sha256(body).hexdigest()[:16], images=[{
        "key": key, "raw": "plot.png", "kind": "png", "path": str(doc.parent / "plot.png"),
        "sha": hashlib.sha256(image).hexdigest()[:16],
    }])
    (tmp_path / "index.json").write_text(json.dumps({"reports": [active, gone]}))
    for _ in range(2):
        assert run("import-reports", str(tmp_path)) == 0
    with closing(reports.owner_connection()) as conn:
        [first] = reports.listing(conn)
        assert (first.title, first.version) == ("Published original", 1)
    client = TestClient(web_server.app, follow_redirects=False)
    image_url = f"/reports/{first.report_id}/images/0"
    assert client.get("/r/original").status_code == 401
    assert client.get(image_url).status_code == 401
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    page = client.get("/r/original")
    assert page.status_code == 200
    assert "<title>Published original</title>" in page.text
    assert "Copy for original." in page.text
    assert image_url in page.text
    assert client.get(image_url).content == image
    assert client.get("/r/withdrawn").status_code == 404
    doc.write_text("# Edited\n\nNew Desk content")
    assert run("sync-reports") == 0
    assert run("import-reports", str(tmp_path)) == 0
    assert "New Desk content" in client.get("/r/original").text
    assert client.get(image_url).status_code == 404
    doc.unlink()
    assert run("sync-reports") == 0
    assert client.get("/r/original").status_code == 404


@pytest.mark.platforms("posix")
def test_unreadable_entries_are_all_named_and_nothing_is_published(desk, tmp_path, capsys):
    records = [record(tmp_path, slug) for slug in ("valid", "missing", "damaged")]
    (tmp_path / "missing.md").unlink()
    (tmp_path / "damaged.md").write_text("# Different bytes")
    (tmp_path / "index.json").write_text(json.dumps({"reports": records}))
    assert run("import-reports", str(tmp_path)) != 0
    messages = capsys.readouterr().err
    assert "missing" in messages and "damaged" in messages
    with closing(reports.owner_connection()) as conn:
        assert reports.listing(conn, include_private=True) == []
