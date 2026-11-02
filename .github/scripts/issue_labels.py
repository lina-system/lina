#!/usr/bin/env python3
"""Keep lina-system issue labels in line with labels.yml and the issue Context lines.

  issue_labels.py sync   [--labels-file PATH] [--dry-run]
  issue_labels.py issues [--issue N] [--all-comments] [--dry-run]

Reads GITHUB_REPOSITORY and GITHUB_TOKEN from the environment.
"""

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

API = "https://api.github.com"
LABELS_URL = "https://raw.githubusercontent.com/lina-system/.github/main/labels.yml"
RUNNERS = ("dsh", "grok-cursor", "agy", "omo")
SIZES = {"S": "size/S", "M": "size/M", "L": "size/L"}
KIND_BY_PREFIX = {
    "feat": "enhancement",
    "fix": "bug",
    "docs": "documentation",
    "research": "research",
    "chore": "maintenance",
    "refactor": "maintenance",
    "test": "maintenance",
    "ci": "maintenance",
    "build": "maintenance",
}
KINDS = set(KIND_BY_PREFIX.values())
READY, BLOCKED = "ready", "blocked"
MARKER = "<!-- lina-labels:runners -->"

DEPENDS_RE = re.compile(r"\bDepends on:[ \t]*([^\n]*)", re.IGNORECASE)
REF_RE = re.compile(r"(?:([\w.-]+/)?([\w.-]+))?#(\d+)")
SIZE_RE = re.compile(r"\bSize:[ \t]*([SML])\b")
RUNNER_RE = re.compile(r"\bRunner:[ \t]*([\w-]+)", re.IGNORECASE)
PREFIX_RE = re.compile(r"^\s*([a-z]+)(?:\([^)]*\))?!?:", re.IGNORECASE)


def parse_depends(body, repo):
    """Return the issues named on the Depends on line as (owner/repo, number), or None without the line."""
    match = DEPENDS_RE.search(body or "")
    if not match:
        return None
    text = re.split(r"\.(?:\s|$)|\b(?:Size|Runner):", match.group(1))[0]
    owner = repo.split("/")[0]
    refs = []
    for ref_owner, ref_repo, number in REF_RE.findall(text):
        target = repo
        if ref_repo:
            target = f"{ref_owner.rstrip('/') or owner}/{ref_repo}"
        refs.append((target, int(number)))
    return refs


def parse_size(body):
    match = SIZE_RE.search(body or "")
    return SIZES[match.group(1)] if match else None


def parse_runner(body):
    match = RUNNER_RE.search(body or "")
    name = match.group(1).lower() if match else None
    return name if name in RUNNERS else None


def kind_for_title(title):
    match = PREFIX_RE.match(title or "")
    return KIND_BY_PREFIX.get(match.group(1).lower()) if match else None


def is_parent_or_roadmap(issue):
    sub = issue.get("sub_issues_summary") or {}
    return sub.get("total", 0) > 0 or (issue.get("title") or "").strip().lower().startswith("roadmap")


def plan_issue(issue, repo, closed):
    """Decide label changes for one issue.

    closed(owner_repo, number) -> True when that issue is closed.
    Returns (add, remove, runners) where runners lists the runner labels when there are two or more,
    an empty list when the warning should be cleared, and None when runners are not handled.
    """
    labels = {label["name"] for label in issue.get("labels", [])}
    body = issue.get("body") or ""
    add, remove = set(), set()
    exempt = is_parent_or_roadmap(issue)

    if issue.get("state") == "closed":
        remove |= labels & {READY, BLOCKED}
    else:
        deps = parse_depends(body, repo)
        if deps is not None:
            state = READY if all(closed(r, n) for r, n in deps) else BLOCKED
            add.add(state)
            remove.add(BLOCKED if state == READY else READY)

    kind = kind_for_title(issue.get("title"))
    if kind and not labels & KINDS:
        add.add(kind)

    size = parse_size(body)
    if size and not labels & set(SIZES.values()):
        add.add(size)

    runners = None
    if not exempt:
        runner = parse_runner(body)
        if runner and not labels & set(RUNNERS):
            add.add(runner)
        if issue.get("state") != "closed":
            present = sorted((labels | add) & set(RUNNERS))
            runners = present if len(present) > 1 else []

    return add - labels, remove & labels, runners


def warning_body(runners):
    names = ", ".join(f"`{name}`" for name in runners)
    return (
        f"{MARKER}\nThis issue has {len(runners)} runner labels ({names}). "
        "Keep exactly one: the runner that will do the work."
    )


def cleared_body():
    return f"{MARKER}\nResolved: this issue now has at most one runner label."


def plan_comment(existing, runners):
    """Return ('create'|'update'|None, body) for the runner warning; existing is the marked comment or None."""
    if runners is None:
        return None, None
    if runners:
        body = warning_body(runners)
    elif existing:
        body = cleared_body()
    else:
        return None, None
    if existing is None:
        return "create", body
    return ("update", body) if existing["body"] != body else (None, None)


def plan_definitions(wanted, current):
    """Return (create, update, extra) from the labels.yml entries and the repository's labels."""
    by_name = {label["name"].lower(): label for label in current}
    create, update = [], []
    for label in wanted:
        color = str(label["color"]).lower().lstrip("#")
        desc = label.get("description") or ""
        have = by_name.get(label["name"].lower())
        if have is None:
            create.append({"name": label["name"], "color": color, "description": desc})
        elif have["name"] != label["name"] or have["color"].lower() != color or (have.get("description") or "") != desc:
            update.append({"old": have["name"], "name": label["name"], "color": color, "description": desc})
    names = {label["name"].lower() for label in wanted}
    extra = sorted(label["name"] for label in current if label["name"].lower() not in names)
    return create, update, extra


class GitHub:
    def __init__(self, token, dry_run):
        self.token = token
        self.dry_run = dry_run

    def request(self, method, path, data=None):
        if method != "GET" and self.dry_run:
            print(f"dry-run: {method} {path} {json.dumps(data) if data else ''}")
            return None
        url = path if path.startswith("http") else API + path
        req = urllib.request.Request(url, method=method, data=json.dumps(data).encode() if data else None)
        req.add_header("Accept", "application/vnd.github+json")
        req.add_header("X-GitHub-Api-Version", "2022-11-28")
        if self.token:
            req.add_header("Authorization", f"Bearer {self.token}")
        with urllib.request.urlopen(req) as resp:
            text = resp.read().decode()
            link = resp.headers.get("Link", "")
        nxt = re.search(r'<([^>]+)>;\s*rel="next"', link)
        return (json.loads(text) if text else None), (nxt.group(1) if nxt else None)

    def get(self, path):
        return self.request("GET", path)[0]

    def get_all(self, path):
        items, url = [], path
        while url:
            page, url = self.request("GET", url)
            items.extend(page)
        return items

    def write(self, method, path, data=None):
        self.request(method, path, data)


def quote(name):
    return urllib.request.quote(name, safe="")


def run_sync(gh, repo, labels_file):
    import yaml

    if labels_file:
        with open(labels_file, encoding="utf-8") as handle:
            text = handle.read()
    else:
        with urllib.request.urlopen(LABELS_URL) as resp:
            text = resp.read().decode()
    wanted = yaml.safe_load(text)
    current = gh.get_all(f"/repos/{repo}/labels?per_page=100")
    create, update, extra = plan_definitions(wanted, current)
    for label in create:
        print(f"create {label['name']}")
        gh.write("POST", f"/repos/{repo}/labels", label)
    for label in update:
        print(f"update {label['old']} -> {label['name']}")
        gh.write("PATCH", f"/repos/{repo}/labels/{quote(label['old'])}",
                 {"new_name": label["name"], "color": label["color"], "description": label["description"]})
    print("not in labels.yml (kept): " + (", ".join(extra) or "none"))
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary:
        with open(summary, "a", encoding="utf-8") as handle:
            handle.write(f"## Labels\n\nCreated {len(create)}, updated {len(update)}.\n\n"
                         f"Not in labels.yml (kept): {', '.join(extra) or 'none'}\n")


def run_issues(gh, repo, only, all_comments):
    issues = [i for i in gh.get_all(f"/repos/{repo}/issues?state=all&per_page=100") if "pull_request" not in i]
    states = {(repo, i["number"]): i["state"] for i in issues}

    def closed(target, number):
        if (target, number) not in states:
            try:
                states[(target, number)] = gh.get(f"/repos/{target}/issues/{number}")["state"]
            except urllib.error.HTTPError as err:
                print(f"warning: cannot read {target}#{number} ({err.code}); counted as open")
                states[(target, number)] = "unknown"
        return states[(target, number)] == "closed"

    for issue in issues:
        number = issue["number"]
        add, remove, runners = plan_issue(issue, repo, closed)
        for name in sorted(add):
            print(f"#{number} add {name}")
        if add:
            gh.write("POST", f"/repos/{repo}/issues/{number}/labels", {"labels": sorted(add)})
        for name in sorted(remove):
            print(f"#{number} remove {name}")
            gh.write("DELETE", f"/repos/{repo}/issues/{number}/labels/{quote(name)}")
        if runners is None or not (runners or all_comments or number == only):
            continue
        comments = gh.get_all(f"/repos/{repo}/issues/{number}/comments?per_page=100")
        existing = next((c for c in comments if MARKER in (c.get("body") or "")), None)
        action, body = plan_comment(existing, runners)
        if action == "create":
            print(f"#{number} warn runners {', '.join(runners)}")
            gh.write("POST", f"/repos/{repo}/issues/{number}/comments", {"body": body})
        elif action == "update":
            print(f"#{number} update runner comment")
            gh.write("PATCH", f"/repos/{repo}/issues/comments/{existing['id']}", {"body": body})


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("mode", choices=["sync", "issues"])
    parser.add_argument("--labels-file")
    parser.add_argument("--issue", type=int)
    parser.add_argument("--all-comments", action="store_true")
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)
    repo = os.environ["GITHUB_REPOSITORY"]
    gh = GitHub(os.environ.get("GITHUB_TOKEN"), args.dry_run)
    if args.mode == "sync":
        run_sync(gh, repo, args.labels_file)
    else:
        run_issues(gh, repo, args.issue, args.all_comments)


if __name__ == "__main__":
    sys.exit(main())
