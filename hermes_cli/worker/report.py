"""The report contract every run is handed, the mode prefixes, and the check of the ``## 다음에 알아야 할 것`` section."""

from __future__ import annotations

import re
from pathlib import Path

MODES = {"ulw": "ulw", "research": "ulw research", "mass": "ulw mass-ulw", "mass-research": "ulw mass research"}
NEXT_NOTES = "## 다음에 알아야 할 것"
_NEXT_NOTES_KEY = "다음에알아야할것"
_EMPTY_NOTES = frozenset({"없음", "없다", "없습니다", "해당 없음", "해당없음", "none", "n/a", "na", "-", "—", "tbd"})
_HEADING = re.compile(r"^ {0,3}#{1,6}\s*(.*?)\s*#*\s*$")

RULES = """\
Rules for this task (from lina worker):
- Work only inside the current directory. Files under input/ are read-only copies; never change anything outside this directory.
- Write report.md in the current directory with these sections: 만든 것 / 확인한 원문(links, files, commands) / 추정 / 미확인 / 끝 조건 대조 / 남은 점 / 다음에 알아야 할 것. Write it in Korean.
- The last section has exactly the heading `## 다음에 알아야 할 것`. Write there what the next task in the same field must know: what you tried that did not work, quirks of machines and tools you met, decisions that are not visible in the result. At least one concrete line; a run that ends without this section, or with it empty, is marked "review" instead of "done".
- Put deliverables under out/. When the brief asks to change a file given under input/, write the full changed file to out/ and a unified diff to out/changes.diff; the agent that handed out the task applies it.
- Do not send messages, publish, push, delete outside this directory, spend money, or change credentials. If the work needs any of these, write it in report.md and stop.
- Never write secrets into files or replies.
"""
REPO_RULES = """\
- This directory is a git worktree on branch {branch}. Commit your changes there with Conventional Commit messages in English. Pushing is blocked; do not open pull requests. report.md, out/ and input/ stay in the worktree root and are not committed (they are in .git/info/exclude).
"""


def first_prompt(mode: str, brief: str, branch: str | None) -> str:
    rules = RULES + (REPO_RULES.format(branch=branch) if branch else "")
    return f"{MODES[mode]} {brief.strip()}\n\n{rules}"


def with_hub_rules(prompt: str, rules: str) -> str:
    """A first prompt for the hub node: ``worker.hub_rules`` after the standard rules; nothing added when empty."""
    return prompt if not rules else prompt.rstrip("\n") + "\n" + rules.rstrip("\n") + "\n"


def follow_up_prompt(mode: str, message: str) -> str:
    """A new run starts with the task's mode prefix, like the first one; a text that already carries it is kept."""
    prefix = MODES.get(mode, MODES["ulw"])
    body = message.lstrip()
    if body == prefix or body.startswith((prefix + " ", prefix + "\n")):
        return message
    return f"{prefix} {message}"


def next_notes(report: Path) -> int | None:
    """Lines with content under ``## 다음에 알아야 할 것`` in *report*; None when the section is missing."""
    try:
        lines = report.read_text(encoding="utf-8-sig", errors="replace").splitlines()
    except OSError:
        return None
    inside, found, count = False, False, 0
    for line in lines:
        heading = _HEADING.match(line)
        if heading:
            if inside:
                break
            inside = re.sub(r"[\s:：*]", "", heading.group(1)) == _NEXT_NOTES_KEY
            found = found or inside
            continue
        text = line.strip().lstrip("-*•·0123456789.) ").strip().strip(".").strip()
        if inside and text and text.lower() not in _EMPTY_NOTES:
            count += 1
    return count if found else None
