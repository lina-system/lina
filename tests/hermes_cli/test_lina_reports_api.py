"""The client API's reports (lina#263, ``hermes_cli/web_routers/lina_reports.py``): ``GET /api/lina/reports/{id}``,
``GET /api/lina/reports?card=&goal=`` and the report page for a plain browser, behind the dashboard's sign-in. A
private report is served only to the owner's session. Served JSON is validated against
``docs/api/work-client.yaml``; the store is the real Postgres of tests/_fixtures/memory_store.py.
"""

from __future__ import annotations

from contextlib import closing
from pathlib import Path

import jsonschema
import pytest
from fastapi import FastAPI, Request
from fastapi.testclient import TestClient

import hermes_state_reports as reports
import hermes_yaml

REPO = Path(__file__).resolve().parents[2]


def _jsonschema(node):
    if isinstance(node, list):
        return [_jsonschema(n) for n in node]
    if not isinstance(node, dict):
        return node
    out = {k: _jsonschema(v) for k, v in node.items() if k != "nullable"}
    if node.get("nullable") and "type" in out:
        out["type"] = [out["type"], "null"]
    return out


_COMPONENTS = _jsonschema(
    hermes_yaml.safe_load((REPO / "docs/api/work-client.yaml").read_text(encoding="utf-8"))["components"])


def _valid(name: str, data):
    jsonschema.Draft7Validator({"$ref": f"#/components/schemas/{name}", "components": _COMPONENTS}).validate(data)
    return data


@pytest.fixture
def client():
    from hermes_cli import web_server
    test_client = TestClient(web_server.app)
    test_client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    return test_client


def _write(agent="alpha", **fields):
    with closing(reports.agent_connection(agent)) as conn:
        return reports.save(conn, agent=agent, **{"title": "Costs", "summary": "Costs fell 12% this week.",
                                                  "body": "First body.", **fields})


def test_a_report_is_served_with_its_newest_body_and_an_earlier_version_on_request(client):
    first = _write(card="t_one", goal="g_one")
    _write(report_id=first.report_id, summary="Costs fell 14% this week.", body="Second body.")

    current = _valid("Report", client.get(f"/api/lina/reports/{first.report_id}").json())
    assert (current["body"], current["summary"], current["version"], current["shownVersion"]) == (
        "Second body.", "Costs fell 14% this week.", 2, 2)
    assert (current["agent"], current["card"], current["goal"]) == ("alpha", "t_one", "g_one")
    assert current["page"] == f"/api/lina/reports/{first.report_id}/page"
    assert [v["version"] for v in current["versions"]] == [1, 2]
    old = _valid("Report", client.get(f"/api/lina/reports/{first.report_id}?version=1").json())
    assert (old["body"], old["summary"], old["shownVersion"]) == ("First body.", "Costs fell 12% this week.", 1)
    assert client.get(f"/api/lina/reports/{first.report_id}?version=3").status_code == 404
    assert client.get("/api/lina/reports/rep_000000000000").status_code == 404


def test_the_list_serves_every_agent_s_reports_of_one_card_and_one_goal(client):
    one = _write(card="t_one", goal="g_one")
    two = _write(agent="beta", card="t_two", goal="g_one")

    every = _valid("ReportList", client.get("/api/lina/reports").json())["reports"]
    assert {r["id"] for r in every} == {one.report_id, two.report_id}
    assert [r["id"] for r in client.get("/api/lina/reports?card=t_two").json()["reports"]] == [two.report_id]
    assert {r["id"] for r in client.get("/api/lina/reports?goal=g_one").json()["reports"]} == {
        one.report_id, two.report_id}
    assert client.get("/api/lina/reports?card=t_one&goal=g_two").json() == {"reports": []}


def test_a_private_report_is_served_to_the_owner_s_session_only():
    from hermes_cli.web_routers import lina_reports
    private = _write(private=True, card="t_one")
    app = FastAPI()
    app.include_router(lina_reports.router)
    other = {"principal": False}

    @app.middleware("http")
    async def principal(request: Request, call_next):
        request.state.token_authenticated = other["principal"]
        return await call_next(request)
    client = TestClient(app)

    assert client.get(f"/api/lina/reports/{private.report_id}").json()["private"] is True
    assert [r["id"] for r in client.get("/api/lina/reports?card=t_one").json()["reports"]] == [private.report_id]
    other["principal"] = True
    assert client.get(f"/api/lina/reports/{private.report_id}").status_code == 404
    assert client.get(f"/api/lina/reports/{private.report_id}/page").status_code == 404
    assert client.get("/api/lina/reports?card=t_one").json() == {"reports": []}


def test_the_page_is_plain_html_behind_the_sign_in_and_shows_untrusted_markdown_safely(client):
    report = _write(title="Disk <b>review</b>", body=(
        "## Disks\n\n| disk | used |\n| --- | --- |\n| a | 91% |\n\n<script>alert(1)</script>\n\n"
        "[bad](javascript:alert(1)) [good](https://example.org/a) ![img](data:text/html,x)\n"))
    path = f"/api/lina/reports/{report.report_id}/page"

    from hermes_cli import web_server
    assert TestClient(web_server.app).get(path).status_code == 401
    page = client.get(path)
    assert page.status_code == 200 and page.headers["content-type"].startswith("text/html")
    assert "script-src" not in page.headers["content-security-policy"]
    assert page.headers["content-security-policy"].startswith("default-src 'none'")
    text = page.text
    assert "<title>Disk &lt;b&gt;review&lt;/b&gt;</title>" in text
    assert "<table>" in text and "<td>91%</td>" in text
    assert "<script>" not in text and "&lt;script&gt;alert(1)&lt;/script&gt;" in text
    assert "javascript:" not in text and "data:text/html" not in text
    assert '<a href="https://example.org/a">good</a>' in text
