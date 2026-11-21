"""Brain citations returned by the Files API, using the Brain tests' disposable store."""

from fastapi import FastAPI
from fastapi.testclient import TestClient
import pytest

from hermes_cli.web_routers import lina_files
from hermes_cli.work import ledger
import hermes_state_source_visibility as visibility
from tests.hermes_cli._work_env import WorkEnv


@pytest.fixture
def client(tmp_path, monkeypatch, _memory_store, memory_store_sql):
    work = WorkEnv(tmp_path, monkeypatch, extra="  store: {backend: postgres}\n")
    monkeypatch.setenv("LINA_HOME", str(work.root))
    monkeypatch.setattr(ledger, "_psql_argv", lambda: ["psql", _memory_store.app_dsn()])
    memory_store_sql("""
        INSERT INTO work.actor VALUES ('owner', 'human', 'Owner') ON CONFLICT DO NOTHING;
        INSERT INTO work.area (id, title, agent_id, created_by) VALUES ('A1', 'Area', 'owner', 'owner')
            ON CONFLICT DO NOTHING;
        INSERT INTO work.repo VALUES ('A1/album', 'A1', 'album', 'file:///unused') ON CONFLICT DO NOTHING;
        INSERT INTO brain.roots (path, mapping) VALUES ('/wiki', '{}');
        INSERT INTO brain.pages (slug, page_id, root_id, path, type, title, compiled_truth, content_sha256)
            SELECT slug, id, root_id, slug || '.md', 'project', upper(slug), '', 'fixture'
            FROM brain.roots CROSS JOIN (VALUES ('alpha', 1), ('beta', 2)) AS pages(slug, id);
    """)
    app = FastAPI()
    app.include_router(lina_files.router)
    with TestClient(app) as client:
        yield client


def test_file_citations(client, memory_store_sql, monkeypatch):
    path = "docs/한:@%_a.md"
    key = f"repo:A1/album:{path}"
    sha = "b" * 40
    memory_store_sql("""
        INSERT INTO brain.timeline (root_id, page_slug, path, line, entry_date, grade, source_ref, text)
        SELECT p.root_id, p.slug, p.path, r.line, r.dated::date, 'owner', r.ref, 'fixture'
        FROM (VALUES ('beta', 3, NULL, %s), ('alpha', 9, '2026-01-02', %s),
                     ('beta', 2, '2026-01-02', %s), ('alpha', 5, '2026-01-02', %s),
                     ('alpha', 6, '2026-01-03', %s), ('alpha', 7, '2026-01-03', %s),
                     ('alpha', 8, '2026-01-03', %s), ('alpha', 10, '2026-01-03', %s))
             AS r(slug, line, dated, ref) JOIN brain.pages p USING (slug)
    """, (key, key + "@" + sha, key, key, key + ".bak", f"repo:A2/album:{path}",
          "repo:A1/album:docs/다른.md", key.replace("%_", "xx") + "@" + sha))

    def lookup():
        response = client.get("/api/lina/files/A1/album/context", params={"path": path})
        assert response.status_code == 200, response.text
        return response.json()["brain"]

    brain = lookup()
    assert brain == {"available": True, "pages": [
        {"slug": slug, "title": title, "path": page, "line": line, "date": dated,
         "grade": "owner", "commit": commit}
        for slug, title, page, line, dated, commit in [
            ("alpha", "ALPHA", "alpha.md", 5, "2026-01-02", None),
            ("alpha", "ALPHA", "alpha.md", 9, "2026-01-02", sha),
            ("beta", "BETA", "beta.md", 2, "2026-01-02", None),
            ("beta", "BETA", "beta.md", 3, None, None),
        ]
    ]}
    # Today's policy hides msg: refs. Exercise a matching row denied by the shared policy too.
    visible_ref = visibility.visible_ref
    monkeypatch.setattr(visibility, "visible_ref", lambda col: f"({visible_ref(col)} AND right({col}, 1) <> 'b')")
    assert [row["line"] for row in lookup()["pages"]] == [5, 2, 3]


def test_file_citation_limit(client, memory_store_sql):
    def lookup():
        response = client.get("/api/lina/files/A1/album/context", params={"path": "docs/a.md"})
        assert response.status_code == 200, response.text
        return response.json()["brain"]

    assert lookup() == {"available": True, "pages": []}
    insert = """
        INSERT INTO brain.timeline (root_id, page_slug, path, line, entry_date, source_ref, text)
        SELECT root_id, slug, path, n, date '2026-01-01' + n, 'repo:A1/album:docs/a.md', ''
        FROM brain.pages CROSS JOIN generate_series(%s::int, %s::int) n WHERE slug = 'alpha'
    """
    memory_store_sql(insert, (1, 50))
    full = lookup()
    assert "truncated" not in full
    assert [row["line"] for row in full["pages"]] == list(range(50, 0, -1))
    memory_store_sql(insert, (51, 51))
    limited = lookup()
    assert limited["truncated"] is True
    assert [row["line"] for row in limited["pages"]] == list(range(51, 1, -1))


def test_unreadable_brain_keeps_work_context(client, memory_store_sql):
    memory_store_sql("REVOKE SELECT ON brain.timeline FROM lina_app")
    try:
        response = client.get("/api/lina/files/A1/album/context", params={"path": "docs/a.md"})
        assert response.status_code == 200, response.text
        assert response.json() == {"area": "A1", "repo": "A1/album", "path": "docs/a.md",
                                   "runs": [], "tasks": [], "goals": [], "artifacts": [],
                                   "brain": {"available": False, "pages": []}}
    finally:
        memory_store_sql("GRANT SELECT ON brain.timeline TO lina_app")
