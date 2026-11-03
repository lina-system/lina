"""A fake GitHub for ``lina work`` (lina#63): answers ``gh api`` reads from a table keyed by endpoint path (the part
before ``?``) and records every write, GraphQL call and ``gh`` command, so a test can assert that nothing merged,
queued or edited unless the gates passed. Nothing here talks to GitHub."""
from __future__ import annotations

import json
from pathlib import Path

FIXTURES = Path(__file__).parent / "fixtures" / "work_next_day"


class FakeGh:
    def __init__(self, routes: dict | None = None, graphql=None, on_run=None):
        self.routes = dict(routes or {})
        self.graphql_answer = graphql or (lambda query, variables: {"data": {}})
        self.on_run = on_run or (lambda args: "")
        self.reads: list[str] = []
        self.writes: list[tuple[str, str, dict | None]] = []
        self.queries: list[tuple[str, dict]] = []
        self.commands: list[list[str]] = []

    def json(self, path: str):
        self.reads.append(path)
        base, _, query = path.partition("?")
        value = self.routes[base]
        return value(query) if callable(value) else value

    def pages(self, path: str) -> list:
        return list(self.json(path) or [])

    def graphql(self, query: str, **variables) -> dict:
        self.queries.append((query, variables))
        return self.graphql_answer(query, variables)

    def write(self, method: str, path: str, payload: dict | None = None):
        self.writes.append((method, path, payload))
        return {"number": 900}

    def run(self, args: list[str], payload: dict | None = None, timeout: float = 120) -> str:
        self.commands.append(list(args))
        return self.on_run(args)


def recorded_day() -> dict:
    return json.loads((FIXTURES / "lina-2026-11-02.json").read_text(encoding="utf-8"))


def day_gh(day: dict) -> FakeGh:
    """The recorded day of lina-system/lina as ``gh api`` answers."""
    repo = day["repo"]

    def runs(query: str):
        found = day["runs"]["workflow_runs"]
        if "status=completed" in query:
            found = [r for r in found if r["status"] == "completed"]
        return {"workflow_runs": found}

    routes = {f"repos/{repo}/issues": day["issues"], f"repos/{repo}/pulls": day["pulls"],
              f"repos/{repo}/branches": day["branches"], f"repos/{repo}/actions/runs": runs,
              f"repos/{repo}/rules/branches/dev": day["rules"]}
    for number, deps in day["blocked_by"].items():
        routes[f"repos/{repo}/issues/{number}/dependencies/blocked_by"] = deps
    return FakeGh(routes)


class FakeRecord:
    def __init__(self, jobs: list[dict] | None = None):
        self._jobs = jobs or []

    def jobs(self) -> list[dict]:
        return list(self._jobs)
