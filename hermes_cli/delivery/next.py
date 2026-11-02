"""``lina work next``: which issues to open now, and why the others wait (docs/delivery.md steps 2 and 5).

``collect`` reads everything once into a snapshot; ``decide`` is a pure function of that snapshot. Only two things
hold a start back: an open issue in the Blocked by list, or work already running on the same contract scope
(the title scope ``feat(<scope>):`` in ``work.contract_scopes``, or a ``Serial with:`` line). There are no caps on
open pull requests or running jobs, and a red ``dev`` holds merges only: it is shown, it never holds a start.
Issues the most others wait on open first, then the longest dependency chain.
"""
from __future__ import annotations

import re
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

from hermes_cli.delivery import settings
from hermes_cli.delivery.github import GhError, dev_state

NEEDS_ROUTING = "needs-routing"
_REF = re.compile(r"(?<![\w-])(?:([\w.-]+)/)?([\w.-]+)(?:#| issue #)(\d+)(?!\d)")
_SERIAL = re.compile(r"^\s*Serial with:(.*)$", re.MULTILINE)
_SERIAL_REF = re.compile(r"(?:(?:[\w.-]+/)?([\w.-]+))?#(\d+)(?!\d)")
_SCOPE = re.compile(r"^\s*[a-z]+\(([^)]+)\)!?:")
_BRANCH = re.compile(r"^[a-z]+/(\d+)-")
_CLOSES = re.compile(r"\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s+#(\d+)\b", re.IGNORECASE)


def collect_repo(gh, full: str, base: str, workflow: str) -> dict:
    repo = {"issues": [], "blocked_by": {}, "pulls": [], "branches": [], "dev_red": None, "error": None}
    try:
        raw = gh.pages(f"repos/{full}/issues?state=open")
        repo["issues"] = [{
            "n": row["number"], "title": row.get("title") or "", "body": row.get("body") or "",
            "labels": [label["name"] if isinstance(label, dict) else label for label in row.get("labels", [])],
            "parent": bool((row.get("sub_issues_summary") or {}).get("total"))}
            for row in raw if not row.get("pull_request")]
        need = [row["number"] for row in raw if not row.get("pull_request") and (
            row.get("issue_dependencies_summary") is None
            or row["issue_dependencies_summary"].get("total_blocked_by", 1))]
        with ThreadPoolExecutor(8) as pool:
            found = pool.map(lambda n: gh.json(f"repos/{full}/issues/{n}/dependencies/blocked_by"), need)
            for n, deps in zip(need, found):
                repo["blocked_by"][n] = [{
                    "repo": (d.get("repository_url") or full).rsplit("/repos/", 1)[-1], "n": d["number"],
                    "state": d.get("state")} for d in deps or []]
        repo["pulls"] = [{"n": p["number"], "head": p["head"]["ref"], "body": p.get("body") or ""}
                         for p in gh.pages(f"repos/{full}/pulls?state=open")]
        repo["branches"] = [b["name"] for b in gh.pages(f"repos/{full}/branches")]
        green, red = dev_state(gh, full, base, workflow)
        repo["dev_red"] = None if green else sorted(red) or [workflow]
    except (GhError, ValueError, KeyError, TypeError) as exc:
        repo["error"] = str(exc) or type(exc).__name__
    return repo


def collect(gh, repos: list[str], record, base: Path | None = None) -> dict:
    """Every outside read in one snapshot; ``decide`` needs nothing else."""
    branch, workflow = settings.base_branch(base), settings.dev_workflow(base)
    with ThreadPoolExecutor(max(1, len(repos))) as pool:
        found = dict(zip(repos, pool.map(lambda full: collect_repo(gh, full, branch, workflow), repos)))
    return {"sprint": settings.sprint(base), "repos": found, "jobs": record.jobs(),
            "runners": settings.runner_labels(base), "scopes": list(settings.contract_scopes(base))}


def _short(full: str) -> str:
    return full.split("/", 1)[-1]


def _ref(key: tuple[str, int], home: str) -> str:
    return f"#{key[1]}" if key[0] == home else f"{key[0]}#{key[1]}"


def scope_of(title: str) -> str:
    match = _SCOPE.match(title)
    return match.group(1).strip() if match else ""


def runner_of(labels: list[str], runners: dict[str, str]) -> str | None:
    if NEEDS_ROUTING in labels:
        return None
    return next((runner for label, runner in runners.items() if label in labels), None)


def _refs(text: str, repos: list[str]) -> list[tuple[str, int]]:
    by_short = {_short(full): full for full in repos}
    found = []
    for owner, name, n in _REF.findall(text or ""):
        full = f"{owner}/{name}" if owner else by_short.get(name)
        if full in repos:
            found.append((full, int(n)))
    return found


def running_work(snapshot: dict) -> dict[tuple[str, int], str]:
    """``{(repo, n): why}`` for every issue already in flight: a job of the work record, a pull request that
    closes it or is named after it, a branch named after it. A running job's reason wins over an ended one."""
    repos = list(snapshot["repos"])
    running: dict[tuple[str, int], str] = {}
    jobs = [job for job in snapshot.get("jobs", []) if job.get("state") != "free"]
    for job in sorted(jobs, key=lambda job: job.get("state") != "running"):
        for key in _refs(job.get("text") or "", repos):
            running.setdefault(key, f"job {job['id']}" + ("" if job["state"] == "running" else " (ended, not merged)"))
    for full, repo in snapshot["repos"].items():
        for pull in repo.get("pulls", []):
            numbers = {int(n) for n in _CLOSES.findall(pull["body"])}
            match = _BRANCH.match(pull["head"])
            if match:
                numbers.add(int(match.group(1)))
            for n in numbers:
                running.setdefault((full, n), f"PR #{pull['n']}")
        for branch in repo.get("branches", []):
            match = _BRANCH.match(branch)
            if match:
                running.setdefault((full, int(match.group(1))), f"branch {branch}")
    return running


def graph(snapshot: dict):
    """Open issues, their open blockers, how many open issues wait behind each and its longest chain."""
    issues = {(full, i["n"]): dict(i, repo=full) for full, repo in snapshot["repos"].items()
              for i in repo.get("issues", [])}
    blockers = {}
    for full, repo in snapshot["repos"].items():
        for n, deps in repo.get("blocked_by", {}).items():
            blockers[(full, int(n))] = [(d["repo"], d["n"]) for d in deps if d.get("state") == "open"]
    children: dict = {key: [] for key in issues}
    for key, ups in blockers.items():
        if key in issues:
            for up in ups:
                if up in issues:
                    children[up].append(key)

    def behind(start):
        seen, stack = set(), list(children.get(start, []))
        while stack:
            key = stack.pop()
            if key not in seen and key != start:
                seen.add(key)
                stack.extend(children.get(key, []))
        return len(seen)

    def depth(key, nexts, memo, path=()):
        if key in memo:
            return memo[key]
        if key in path:
            return 0
        memo[key] = 1 + max([depth(k, nexts, memo, path + (key,)) for k in nexts(key)] or [0])
        return memo[key]

    def down(key):
        return children.get(key, [])

    def up(key):
        return [k for k in blockers.get(key, []) if k in issues]

    up_memo: dict = {}
    down_memo: dict = {}
    waiting = {key: behind(key) for key in issues}
    chain = {key: depth(key, up, up_memo) + depth(key, down, down_memo) - 1 for key in issues}
    return issues, blockers, waiting, chain


def _serial(body: str, home: str, repos: list[str]) -> list[tuple[str, int]]:
    by_short = {_short(full): full for full in repos}
    refs = []
    for line in _SERIAL.findall(body):
        for name, n in _SERIAL_REF.findall(line):
            refs.append((by_short.get(name, home) if name else home, int(n)))
    return refs


def decide(snapshot: dict) -> dict:
    """Pure: the snapshot in, the plan out. Each decision carries ``kind``: ``open`` or why it waits
    (``parent``, ``sprint``, ``running``, ``blocked``, ``stopped``, ``scope``, ``unrouted``)."""
    sprint, scopes, repos = snapshot.get("sprint"), set(snapshot.get("scopes") or ()), list(snapshot["repos"])
    running = running_work(snapshot)
    issues, blockers, waiting, chain = graph(snapshot)
    longest = max(chain.values(), default=0)
    taken = {}
    for key in running:
        scope = scope_of(issues[key]["title"]) if key in issues else ""
        if scope in scopes:
            taken.setdefault((key[0], scope), (key, ""))
    in_flight = dict(running)
    decisions = []
    for key in sorted(issues, key=lambda k: (-waiting[k], -chain[k], repos.index(k[0]), k[1])):
        issue, full = issues[key], key[0]
        base = {"repo": full, "issue": key[1], "title": issue["title"], "waiting": waiting[key], "chain": chain[key],
                "longest": chain[key] == longest, "runner": runner_of(issue["labels"], snapshot.get("runners") or {})}

        def hold(kind, reason, shown=True):
            decisions.append(dict(base, action="skip", kind=kind, reason=reason, shown=shown))

        scope = scope_of(issue["title"])
        holder = taken.get((full, scope)) if scope in scopes else None
        serial = [r for r in _serial(issue["body"], full, repos) if r in in_flight]
        if issue["parent"] or issue["title"].strip() == "Roadmap":
            hold("parent", "parent issue" if issue["parent"] else "Roadmap", shown=False)
        elif not settings.in_sprint(issue["body"], sprint):
            found = settings.issue_sprint(issue["body"])
            hold("sprint", "not in the current sprint: " + (settings.sprint_label(found) if found else "no sprint"),
                 shown=False)
        elif key in running:
            hold("running", f"already running: {running[key]}")
        elif blockers.get(key):
            hold("blocked", "open dependency: " + ", ".join(_ref(b, full) for b in blockers[key]))
        elif snapshot["repos"][full].get("error"):
            hold("stopped", f"cannot read {full}: {snapshot['repos'][full]['error']}", shown=False)
        elif holder:
            hold("scope", f"same contract running: {scope} {_ref(holder[0], full)}" + (f" {holder[1]}" if holder[1] else ""))
        elif serial:
            hold("scope", "same contract running: Serial with " + ", ".join(_ref(r, full) for r in serial))
        elif not base["runner"]:
            hold("unrouted", "not routed yet (" + (NEEDS_ROUTING if NEEDS_ROUTING in issue["labels"]
                                                   else "no runner label") + ")")
        else:
            if scope in scopes:
                taken[(full, scope)] = (key, "opened now")
            in_flight[key] = "opened now"
            decisions.append(dict(base, action="open", kind="open", reason="", shown=True))
    red = {full: repo["dev_red"] for full, repo in snapshot["repos"].items() if repo.get("dev_red")}
    return {"sprint": sprint, "red": red, "decisions": decisions, "running": len(running)}


def line_of(decision: dict) -> str:
    name = f"{decision['repo']}#{decision['issue']}"
    if decision["action"] == "open":
        text = f"open {name} ({decision['runner']}, {decision['waiting']} waiting behind it)"
        result = decision.get("submit")
        if result:
            text += f" -> {result['id']}" if result.get("ok") else f" -> failed: {result['why']}"
        return text
    return f"hold {name} ({decision['reason']})"


def lines_of(plan: dict) -> list[str]:
    lines = [f"dev red in {full}: merges wait, starts do not ({', '.join(checks)})" for full, checks in
             plan["red"].items()]
    if plan["sprint"] is None:
        lines.append("no current sprint: set it with `lina work sprint set <S>`")
    lines += [line_of(d) for d in plan["decisions"] if d["shown"]]
    return lines if any(d["shown"] for d in plan["decisions"]) or lines else ["nothing to open"]


def brief_of(full: str, n: int, base: str, author: str | None) -> str:
    return (f"# {full}#{n}\n\n"
            f"Do {full} issue #{n}. First read the issue body, its Done when, its parent issue and its dependencies "
            f"with `gh issue view {n} -R {full}`. Base branch is `{base}`.\n"
            f"- Branch `<type>/{n}-<short-name>`, pull request into `{base}`. Body: Summary, `Closes #{n}`, the "
            "issue's Done when with each box checked, Verification (the commands you ran and what you saw).\n"
            + (f"- Commit author `{author}`.\n" if author else "")
            + "- Public repository: no personal paths, machine names, people's names or secrets in code, docs or "
              "pull request text.\n"
              "- Stay inside the issue's scope. Write anything found outside it in report.md under \"남은 점\".\n"
              "- If you would ask a person something, write it in report.md under \"남은 점\" and do as much as you "
              "can.\n")


def submit(plan: dict, base: Path | None = None, hub=None) -> bool:
    """Hand each pick to its runner through ``lina worker`` (#35). Inside a work card (``HERMES_KANBAN_TASK``)
    the hub records each run on that card. True when every hand-off worked."""
    from hermes_cli.worker import settings as worker_settings
    from hermes_cli.worker.hub import Hub, HubError
    from hermes_cli.worker.runners.base import RunnerError
    from hermes_cli.worker.transport import TransportError
    briefs = settings.lina_root(base) / "work" / "briefs"
    briefs.mkdir(parents=True, exist_ok=True)
    ok = True
    for decision in (d for d in plan["decisions"] if d["action"] == "open"):
        full, n = decision["repo"], decision["issue"]
        path = briefs / f"{full.replace('/', '-')}-{n}.md"
        path.write_text(brief_of(full, n, settings.base_branch(base), settings.author(base)), encoding="utf-8")
        said: list[str] = []
        try:
            (hub or Hub(worker_settings.root(base), out=said.append)).submit(
                {"briefs": [str(path)], "mode": "ulw", "repo": full, "runner": decision["runner"],
                 "topic": f"{full}#{n}", "wait": None})
        except (HubError, RunnerError, TransportError, OSError) as exc:
            decision["submit"] = {"ok": False, "why": str(exc)[:200]}
            ok = False
            continue
        decision["submit"] = {"ok": True, "id": (said[0].split() or ["handed out"])[0] if said else "handed out"}
    return ok
