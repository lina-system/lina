"""The size check of a landing (``lina work merge --branch``): a change bigger than its issue's size.

The landed change (the branch against its base, after the base was merged in) is compared with the issue's size
(``size/*`` label, else its ``Size:`` line): S holds 2 files and 150 changed lines, M 8 files and 800, L anything.
A bigger change adds one comment to the issue per pull request (found again by its marker) and one line to the
landing output. The check never stops a landing: a failed read or write is one line too.
"""
from __future__ import annotations

from hermes_cli.delivery.github import GhError

LIMITS = {"S": (2, 150), "M": (8, 800)}


def measure(numstat: str) -> tuple[int, int]:
    """``(files, added + deleted lines)`` of ``git diff --numstat`` output; a binary file counts no lines."""
    files = lines = 0
    for row in numstat.splitlines():
        added, deleted, _path = row.split("\t", 2)
        files += 1
        lines += sum(int(n) for n in (added, deleted) if n.isdigit())
    return files, lines


def _marker(pr: int) -> str:
    return f"<!-- lina-work-size pr={pr} -->"


def note(gh, repo: str, issue: int, pr: int, change: tuple[int, int]) -> str | None:
    """The landing line for a change bigger than issue ``issue``'s size, commenting once per pull request."""
    from hermes_cli.delivery.next import size_of
    try:
        found = gh.json(f"repos/{repo}/issues/{issue}") or {}
        size = size_of([label.get("name", "") for label in found.get("labels") or []], found.get("body") or "")
        if size not in LIMITS or (change[0] <= LIMITS[size][0] and change[1] <= LIMITS[size][1]):
            return None
        files, lines = LIMITS[size]
        said = (f"#{pr} changes {change[0]} files and {change[1]} lines, bigger than size/{size} "
                f"({files} files and {lines} lines)")
        if any(_marker(pr) in (c.get("body") or "") for c in gh.pages(f"repos/{repo}/issues/{issue}/comments")):
            return f"#{issue}: {said}; already noted on the issue"
        gh.write("POST", f"repos/{repo}/issues/{issue}/comments", {"body": f"Size check: {said}.\n\n{_marker(pr)}\n"})
        return f"#{issue}: {said}; noted on the issue"
    except GhError as exc:
        return f"#{issue}: size check skipped: {exc}"
