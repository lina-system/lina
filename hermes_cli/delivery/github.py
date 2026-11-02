"""GitHub reads and writes through ``gh`` for ``lina work``. One ``Gh`` per command; tests hand in a fake."""
from __future__ import annotations

import json
import subprocess


class GhError(RuntimeError):
    pass


class Gh:
    def __init__(self, exe: str = "gh"):
        self.exe = exe

    def run(self, args: list[str], payload: dict | None = None, timeout: float = 120) -> str:
        try:
            done = subprocess.run([self.exe, *args], input=json.dumps(payload) if payload is not None else "",
                                  capture_output=True, text=True, encoding="utf-8", errors="replace",
                                  timeout=timeout, check=False)
        except (OSError, subprocess.SubprocessError) as exc:
            raise GhError(f"gh could not run ({type(exc).__name__})") from None
        if done.returncode != 0:
            # gh's stderr can carry host or credential details: keep the command and its last line only.
            last = (done.stderr.strip().splitlines() or [""])[-1][:200]
            raise GhError(f"gh {' '.join(args[:2])} exited {done.returncode}: {last}".rstrip(": "))
        return done.stdout

    def json(self, path: str):
        return json.loads(self.run(["api", path]) or "null")

    def pages(self, path: str) -> list:
        rows, page = [], 1
        while True:
            joiner = "&" if "?" in path else "?"
            chunk = self.json(f"{path}{joiner}per_page=100&page={page}") or []
            rows.extend(chunk)
            if len(chunk) < 100:
                return rows
            page += 1

    def graphql(self, query: str, **variables) -> dict:
        args = ["api", "graphql", "-f", f"query={query}"]
        for name, value in variables.items():
            args += ["-F" if isinstance(value, int) else "-f", f"{name}={value}"]
        return json.loads(self.run(args) or "null")

    def write(self, method: str, path: str, payload: dict | None = None):
        out = self.run(["api", "-X", method, path] + (["--input", "-"] if payload is not None else []), payload)
        return json.loads(out) if out.strip() else None


def check_runs(gh, repo: str, sha: str) -> list[dict]:
    """Every check run on a commit, not only GitHub's latest per name: the merge rule picks the deciding one."""
    runs, page = [], 1
    while True:
        found = gh.json(f"repos/{repo}/commits/{sha}/check-runs?per_page=100&filter=all&page={page}") or {}
        runs += found.get("check_runs") or []
        if len(runs) >= int(found.get("total_count") or 0) or not found.get("check_runs"):
            return runs
        page += 1


def rule_types(gh, repo: str, branch: str) -> list[dict]:
    return gh.json(f"repos/{repo}/rules/branches/{branch}") or []


def required_contexts(rules: list[dict]) -> list[str]:
    found = []
    for rule in rules:
        if rule.get("type") == "required_status_checks":
            for check in (rule.get("parameters") or {}).get("required_status_checks") or []:
                if check.get("context") not in found:
                    found.append(check["context"])
    return found


def merge_queue_on(rules: list[dict]) -> bool:
    return any(rule.get("type") == "merge_queue" for rule in rules)


def dev_state(gh, repo: str, branch: str, workflow: str) -> tuple[bool, set[str]]:
    """``(green, red job names)`` of the newest finished push run of ``workflow`` on ``branch`` that has a verdict.
    A run cancelled because a newer push took its place, or skipped, says nothing; no run at all is green."""
    runs = (gh.json(f"repos/{repo}/actions/runs?branch={branch}&event=push&status=completed&per_page=20") or {})
    runs = [r for r in runs.get("workflow_runs") or []
            if r.get("name") == workflow and r.get("conclusion") not in ("cancelled", "skipped")]
    if not runs:
        return True, set()
    newest = max(runs, key=lambda r: r.get("created_at") or "")
    if newest.get("conclusion") == "success":
        return True, set()
    from hermes_cli.merge_rules import RED
    jobs = (gh.json(f"repos/{repo}/actions/runs/{newest['id']}/jobs?per_page=100") or {}).get("jobs") or []
    return False, {job["name"] for job in jobs if job.get("conclusion") in RED}
