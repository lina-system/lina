"""Review follow-ups (docs/delivery.md step 8): unresolved Codex P2/P3 findings of a merged pull request go into one
open issue (label ``follow-up``) per area and destination milestone, each finding a checkbox with its pull request; a
finding already listed in an open bundle of that area (same path, same title) is not added again, so a second run
adds nothing.

The source milestone is the milestone of the issue the pull request closes; the destination is the next open
milestone after it in roadmap order (``M0``, ``M1``, ... by title). Without a source milestone or an open one after
it, the findings go to the area's bundle with no milestone, for the owner to place. The runtime's current sprint plays
no part. The area is a path's first directory (two for ``work.followup_two_level``, ``repo`` for a root file).
"""
from __future__ import annotations

import re
from pathlib import Path

from hermes_cli import merge_rules as rules
from hermes_cli.delivery import settings
from hermes_cli.delivery.github import GhError

LABEL = "follow-up"
_ROADMAP = re.compile(r"^\s*M(\d+)\b")
_BADGE = re.compile(r"!\[(P[0-3]) Badge\]")
_IMAGE = re.compile(r"!\[[^\]]*\]\([^)]*\)")
_BOX = re.compile(r"^- \[[ xX]\] (P[23]) (.*) — (\S+):(\d+) \(from #(\d+)\)\s*$")
_SIZES = (("S", 3), ("M", 8))
THREADS = """query($owner:String!,$name:String!,$number:Int!,$after:String){repository(owner:$owner,name:$name){
pullRequest(number:$number){reviewThreads(first:100,after:$after){pageInfo{hasNextPage endCursor}
nodes{isResolved comments(first:1){nodes{databaseId}}}}}}}"""
THREAD_PAGES = 20


def title_of(body: str) -> str:
    for line in (body or "").splitlines():
        text = re.sub(r"<[^>]+>", "", _IMAGE.sub("", line)).replace("**", "").replace("__", "").strip()
        if text:
            return text
    return ""


def normal(title: str) -> str:
    return re.sub(r"\s+", " ", re.sub(r"[`*_]", "", title)).strip().lower().rstrip(" .!?:;")


def area_of(path: str, two_level: tuple[str, ...] = ()) -> str:
    parts = path.split("/")
    if "/".join(parts[:2]) in two_level and len(parts) > 2:
        return "/".join(parts[:2])
    return parts[0] if len(parts) > 1 else "repo"


def size_of(boxes: int) -> str:
    return next((size for size, most in _SIZES if boxes <= most), "L")


def _codex_p23(c: dict) -> str | None:
    """``P2``/``P3`` for a Codex thread-opening line comment with that badge, else None."""
    badge = _BADGE.search(c.get("body") or "")
    if (not str((c.get("user") or {}).get("login", "")).startswith(rules.CODEX) or c.get("in_reply_to_id")
            or not badge or badge.group(1) not in ("P2", "P3") or not c.get("path")):
        return None
    return badge.group(1)


def findings(comments: list[dict], head: str, resolved: set[int]) -> list[dict]:
    """Unresolved Codex P2/P3 thread-opening line comments, on the merged head or an older one (``older``): a head
    that moved on does not settle a finding; only a resolved thread does."""
    found = []
    for c in comments:
        level = _codex_p23(c)
        if level is None or c.get("id") in resolved:
            continue
        found.append({"id": c["id"], "level": level, "title": title_of(c["body"]), "path": c["path"],
                      "line": c.get("line") or c.get("original_line") or 0,
                      "older": head not in (c.get("commit_id"), c.get("original_commit_id"))})
    return found


def _older_resolved(comments: list[dict], head: str, resolved: set[int]) -> int:
    return sum(1 for c in comments if _codex_p23(c) and c.get("id") in resolved
               and head not in (c.get("commit_id"), c.get("original_commit_id")))


def _rank(milestone: dict | None) -> int | None:
    match = _ROADMAP.match(str((milestone or {}).get("title") or ""))
    return int(match[1]) if match else None


def source_milestone(gh, repo: str, pr: int) -> dict | None:
    """The milestone of the issue the pull request closes; the earliest in roadmap order when it closes several."""
    found = []
    for number in rules.closed_refs(repo, (gh.json(f"repos/{repo}/pulls/{pr}") or {}).get("body")):
        try:
            milestone = (gh.json(f"repos/{repo}/issues/{number}") or {}).get("milestone")
        except GhError:
            continue
        if milestone:
            found.append(milestone)
    ranked = [m for m in found if _rank(m) is not None]
    return min(ranked, key=_rank) if ranked else (found[0] if found else None)


def destination(milestones: list[dict], source: dict | None) -> dict | None:
    """The first open milestone after ``source`` in roadmap order; None without a source or an open one after it."""
    after = _rank(source)
    if after is None:
        return None
    later = [m for m in milestones if (_rank(m) or -1) > after and m.get("state") == "open"]
    return min(later, key=_rank) if later else None


def box(item: dict, pr: int) -> str:
    return f"- [ ] {item['level']} {item['title']} — {item['path']}:{item['line']} (from #{pr})"


def listed(body: str) -> set[tuple[str, str]]:
    return {(m[3], normal(m[2])) for m in (_BOX.match(line) for line in rules.section(body, "Done when")) if m}


def new_body(area: str, milestone: str | None, pr: int, boxes: list[str]) -> str:
    where = f"for milestone {milestone}" if milestone else "with no milestone to place them in yet"
    return (f"## Summary\n\nCodex P2/P3 review findings in `{area}` left after merged pull requests, collected "
            f"{where}.\n\n## Done when\n\n" + "".join(b + "\n" for b in boxes)
            + f"\n## Context\n\nMilestone: {milestone or 'none'}\nSize: {size_of(len(boxes))}\nFollow-up to #{pr}\n")


def added_body(body: str, pr: int, boxes: list[str]) -> str:
    """The issue body with the boxes at the end of Done when, Size recounted and ``Follow-up to #<pr>`` once."""
    lines = body.rstrip("\n").splitlines()
    head = next((i for i, line in enumerate(lines) if re.match(r"^##\s*Done when\s*$", line, re.IGNORECASE)), None)
    if head is None:
        lines += ["", "## Done when", ""]
        head = len(lines) - 2
    end = next((i for i in range(head + 1, len(lines)) if lines[i].startswith("## ")), len(lines))
    last = max((i for i in range(head + 1, end) if lines[i].startswith("- [")), default=head + 1)
    lines[last + 1:last + 1] = boxes
    text = "\n".join(lines) + "\n"
    total = sum(1 for line in rules.section(text, "Done when") if re.match(r"^- \[[ xX]\] ", line))
    text = re.sub(r"^Size:\s*\S+", f"Size: {size_of(total)}", text, count=1, flags=re.MULTILINE)
    if not re.search(rf"^Follow-up to #{pr}\b", text, re.MULTILINE):
        text = text.rstrip("\n") + f"\nFollow-up to #{pr}\n"
    return text


def _resolved(gh, repo: str, pr: int) -> set[int]:
    """First-comment ids of the resolved review threads, every page (a busy pull request has more than 100)."""
    owner, name = repo.split("/")
    resolved, after = set(), None
    for _ in range(THREAD_PAGES):
        variables = {"owner": owner, "name": name, "number": pr, **({"after": after} if after else {})}
        threads = gh.graphql(THREADS, **variables)["data"]["repository"]["pullRequest"]["reviewThreads"]
        resolved |= {c["databaseId"] for node in threads["nodes"] if node["isResolved"]
                     for c in node["comments"]["nodes"]}
        page = threads.get("pageInfo") or {}
        if not page.get("hasNextPage"):
            break
        after = page.get("endCursor")
    return resolved


LOCK = "work-followups"


def file(gh, repo: str, pr: int, head: str, base: Path | None = None) -> list[str]:
    """Create or extend the follow-up issues for one merged pull request; one line per action.

    ``head`` is the merged head: findings on it and unresolved ones on older heads are filed alike, and the older ones
    are accounted for in one line (resolved, already tracked, filed). Each write replaces a whole issue body, so two
    merges filing into the same issue at once would keep only the later one's findings (or open two issues). Reading
    the open follow-up issues and writing them happen under the worker hub's file lock (``Store.lock``, under the
    LINA root), the lock ``lina work next --submit`` uses."""
    comments = gh.pages(f"repos/{repo}/pulls/{pr}/comments")
    resolved = _resolved(gh, repo, pr)
    items, fixed = findings(comments, head, resolved), _older_resolved(comments, head, resolved)
    if not items:
        return [f"#{pr}: no P2/P3 finding to follow up"] + _older_line(pr, fixed, 0, 0)
    source = source_milestone(gh, repo, pr)
    target = destination(gh.pages(f"repos/{repo}/milestones?state=all"), source) if source else None
    from hermes_cli.worker import settings as worker_settings
    from hermes_cli.worker.hub_store import Store
    with Store(worker_settings.root(base)).lock(LOCK):
        lines, tracked, filed = _file_locked(gh, repo, pr, items, target, base)
    return lines + _older_line(pr, fixed, tracked, filed)


def _older_line(pr: int, fixed: int, tracked: int, filed: int) -> list[str]:
    if not (fixed or tracked or filed):
        return []
    return [f"#{pr}: P2/P3 findings on older heads: {fixed} resolved, {tracked} already tracked, {filed} filed"]


def _milestone_number(issue: dict) -> int | None:
    return (issue.get("milestone") or {}).get("number")


def _file_locked(gh, repo: str, pr: int, items: list[dict], target: dict | None,
                 base: Path | None) -> tuple[list[str], int, int]:
    open_issues = [i for i in gh.pages(f"repos/{repo}/issues?state=open&labels={LABEL}") if not i.get("pull_request")]
    groups: dict[str, list[dict]] = {}
    for item in items:
        groups.setdefault(area_of(item["path"], settings.followup_two_level(base)), []).append(item)
    number = (target or {}).get("number")
    lines, tracked, filed = [], 0, 0
    for area, found in groups.items():
        title = f"chore({area}): review follow-ups"
        bundles = [i for i in open_issues if i["title"].strip() == title]
        issue = next((i for i in bundles if _milestone_number(i) == number), None)
        # A finding listed in any open bundle of the area is tracked: the bundle of an earlier milestone or sprint too.
        seen = set().union(*(listed(i.get("body") or "") for i in bundles))
        fresh = []
        for item in found:
            key = (item["path"], normal(item["title"]))
            if key in seen:
                tracked += item["older"]
                continue
            seen.add(key)
            fresh.append(item)
            filed += item["older"]
        if not fresh:
            lines.append(f"{title}: every finding already listed")
            continue
        boxes = [box(item, pr) for item in fresh]
        if issue:
            gh.write("PATCH", f"repos/{repo}/issues/{issue['number']}", {"body": added_body(issue["body"], pr, boxes)})
            lines.append(f"{title}: added {len(boxes)} to #{issue['number']}")
        else:
            payload = {"title": title, "body": new_body(area, (target or {}).get("title"), pr, boxes),
                       "labels": [LABEL], **({"milestone": number} if number else {})}
            made = gh.write("POST", f"repos/{repo}/issues", payload)
            lines.append(f"{title}: opened #{made['number']} with {len(boxes)}")
    return lines, tracked, filed
