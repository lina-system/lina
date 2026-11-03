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


DEV_RUN_PAGES = 10  # 100 runs a page: other workflows (Honcho, LCM-X, CodeQL) push runs onto the same branch


def dev_state(gh, repo: str, branch: str, workflow: str) -> tuple[bool, set[str]]:
    """``(green, red job names)`` of ``branch`` from the newest finished push run of ``workflow`` with a verdict.
    A run cancelled because a newer push took its place, or skipped, says nothing; no run of ``workflow`` at all
    (a new repository) is green. A run still going on a newer push is not waited on (the merge queue tests dev
    with the pull request), as in the gate's ``pick_dev_run``.
    The runs list holds every workflow's runs, newest first, so the pages are read until a deciding run of
    ``workflow`` shows up; one page once missed a red CI behind other workflows' runs. Runs are read in every
    status: a failed run whose jobs are re-run is ``in_progress`` again (``run_attempt`` > 1) and still has no
    green, so it decides as red instead of letting an older green run decide."""
    newest = None
    for page in range(1, DEV_RUN_PAGES + 1):
        found = (gh.json(f"repos/{repo}/actions/runs?branch={branch}&event=push"
                         f"&per_page=100&page={page}") or {}).get("workflow_runs") or []
        deciding = [r for r in found if r.get("name") == workflow and (
            r.get("conclusion") not in (None, "cancelled", "skipped")
            or (r.get("status") != "completed" and int(r.get("run_attempt") or 1) > 1))]
        if deciding:
            newest = max(deciding, key=lambda r: (r.get("created_at") or "", int(r.get("id") or 0)))
            break
        if len(found) < 100:
            break
    else:
        raise GhError(f"no finished {workflow} push run on {branch} in the newest {DEV_RUN_PAGES * 100} runs")
    if newest is None:
        return True, set()
    if newest.get("status") != "completed":
        return False, {f"{workflow} re-run of a red run, still going"}
    if newest.get("conclusion") == "success":
        return True, set()
    from hermes_cli.merge_rules import RED
    jobs = (gh.json(f"repos/{repo}/actions/runs/{newest['id']}/jobs?per_page=100") or {}).get("jobs") or []
    return False, {job["name"] for job in jobs if job.get("conclusion") in RED}
