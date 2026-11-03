"""Shared body of the vendored-subtree checks (``check_honcho_subtree.py``, ``check_lcm_x_subtree.py``).

A vendored tree is a squashed ``git subtree`` with an intake ledger (docs/honcho.md, docs/lcm-x.md). The
squash commit git subtree writes holds upstream's tree exactly, so it is the reference the working tree
is compared with:

* the last row of the ledger's ``## Intakes`` table names the upstream commit of the newest squash under
  the prefix (its ``git-subtree-split`` trailer);
* every upstream LICENSE / COPYING / NOTICE file is still present and byte for byte unchanged;
* every copyright or SPDX header line of an upstream file is still at the top of that file;
* when the subtree needs one (AGPL-3.0 section 5(a): Honcho), ``README.md`` starts with the LINA
  modification notice, and the notice names the ledger's upstream commit.

With ``--mirror REF`` (CI passes the fetched upstream mirror branch) the ledger commit must also be on
the mirror and have the squash commit's tree, so the squash is upstream's own tree.

Exit codes: 0 = clean, 1 = at least one finding, 2 = git or usage error.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path

NOTICE_START = "<!-- lina-modification-notice:start -->"
NOTICE_END = "<!-- lina-modification-notice:end -->"
_SHA = re.compile(r"\b[0-9a-f]{40}\b")
_LICENSE_NAME = re.compile(r"^(LICENSE|LICENCE|COPYING|NOTICE)([.-].*)?$", re.IGNORECASE)
_HEADER_LINE = re.compile(r"copyright|spdx-license-identifier", re.IGNORECASE)
HEADER_LINES = 30  # how far from the top an upstream header line is looked for
HEADER_SLACK = 30  # extra lines LINA may put above it without it counting as moved


@dataclass(frozen=True)
class Subtree:
    """One vendored subtree: where it lives, its ledger, and whether it carries a modification notice."""

    name: str  # the script name used in messages
    prefix: str
    ledger: str
    mirror: str  # the upstream mirror branch, for --help
    modification_notice: bool


class GitError(RuntimeError):
    pass


def _git(repo: Path, *args: str) -> str:
    proc = subprocess.run(["git", *args], cwd=repo, capture_output=True, text=True, encoding="utf-8",
                          errors="surrogateescape", stdin=subprocess.DEVNULL, timeout=300, check=False)
    if proc.returncode != 0:
        raise GitError(f"git {' '.join(args)}: {proc.stderr.strip()}")
    return proc.stdout


def ledger_commit(text: str) -> str | None:
    found = None
    in_intakes = False
    for line in text.splitlines():
        if line.startswith("## "):
            in_intakes = line.strip() == "## Intakes"
        elif in_intakes and line.lstrip().startswith("|"):
            shas = _SHA.findall(line)
            if shas:
                found = shas[0]
    return found


def newest_squash(repo: Path, rev: str, prefix: str) -> tuple[str, str] | None:
    out = _git(repo, "log", "--format=%H%x00%B%x01", f"--grep=^git-subtree-dir: {prefix}/*$", rev)
    for record in out.split("\x01"):
        if "\x00" not in record:
            continue
        commit, body = record.split("\x00", 1)
        if not re.search(rf"^git-subtree-dir: {re.escape(prefix)}/?$", body, re.MULTILINE):
            continue
        split = re.search(r"^git-subtree-split: ([0-9a-f]{40})$", body, re.MULTILINE)
        if split:
            return commit.strip(), split.group(1)
    return None


def upstream_blobs(repo: Path, tree_ish: str) -> dict[str, bytes]:
    listing = _git(repo, "ls-tree", "-r", "-z", tree_ish)
    entries = []
    for entry in listing.split("\0"):
        if entry:
            meta, path = entry.split("\t", 1)
            mode, kind, sha = meta.split()
            if kind == "blob" and mode != "120000":
                entries.append((path, sha))
    proc = subprocess.run(["git", "cat-file", "--batch"], cwd=repo, input="".join(f"{s}\n" for _, s in entries).encode(),
                          capture_output=True, stdin=None, timeout=300, check=False)
    if proc.returncode != 0:
        raise GitError(f"git cat-file --batch: {proc.stderr.decode(errors='replace').strip()}")
    blobs: dict[str, bytes] = {}
    data, pos = proc.stdout, 0
    for path, _ in entries:
        eol = data.index(b"\n", pos)
        size = int(data[pos:eol].split()[2])
        blobs[path] = data[eol + 1:eol + 1 + size]
        pos = eol + 1 + size + 1
    return blobs


def header_lines(content: bytes, limit: int) -> list[str]:
    lines = content.decode("utf-8", errors="replace").splitlines()[:limit]
    return [line.strip() for line in lines if _HEADER_LINE.search(line)]


def check_files(root: Path, upstream: dict[str, bytes], prefix: str) -> list[str]:
    findings = []
    for path, content in sorted(upstream.items()):
        local = root / path
        is_license = bool(_LICENSE_NAME.match(Path(path).name))
        headers = header_lines(content, HEADER_LINES) if b"\0" not in content[:8000] else []
        if not is_license and not headers:
            continue
        if not local.is_file():
            findings.append(f"{prefix}/{path}: upstream {'license file' if is_license else 'file with a license header'}"
                            " is missing")
            continue
        current = local.read_bytes()
        if is_license and current != content:
            findings.append(f"{prefix}/{path}: license file differs from upstream")
        kept = set(header_lines(current, HEADER_LINES + HEADER_SLACK))
        for line in headers:
            if line not in kept:
                findings.append(f"{prefix}/{path}: upstream header line changed or removed: {line!r}")
    return findings


def check_notice(readme: Path, commit: str, prefix: str) -> list[str]:
    if not readme.is_file():
        return [f"{prefix}/README.md is missing"]
    text = readme.read_text(encoding="utf-8-sig", errors="replace")
    if not text.startswith(NOTICE_START) or NOTICE_END not in text:
        return [f"{prefix}/README.md must start with the LINA modification notice ({NOTICE_START} ... {NOTICE_END})"]
    block = text[:text.index(NOTICE_END)]
    if commit not in block:
        return [f"{prefix}/README.md: the modification notice does not name the ledger's upstream commit {commit}"]
    return []


def check_mirror(repo: Path, mirror: str, commit: str, squash: str) -> list[str]:
    tip = _git(repo, "rev-parse", "--verify", f"{mirror}^{{commit}}").strip()
    ancestor = subprocess.run(["git", "merge-base", "--is-ancestor", commit, tip], cwd=repo, capture_output=True,
                              stdin=subprocess.DEVNULL, timeout=60, check=False)
    if ancestor.returncode != 0:
        return [f"upstream commit {commit} is not on the mirror {mirror}"]
    if _git(repo, "rev-parse", f"{commit}^{{tree}}").strip() != _git(repo, "rev-parse", f"{squash}^{{tree}}").strip():
        return [f"squash commit {squash} does not hold the tree of upstream commit {commit}"]
    return []


def run(subtree: Subtree, repo: Path, rev: str, mirror: str | None) -> list[str]:
    ledger = repo / subtree.ledger
    if not ledger.is_file():
        return [f"{subtree.ledger} is missing"]
    commit = ledger_commit(ledger.read_text(encoding="utf-8-sig"))
    if commit is None:
        return [f"{subtree.ledger} has no intake row with a full upstream commit sha"]
    squash = newest_squash(repo, rev, subtree.prefix)
    if squash is None:
        return [f"no git subtree squash for {subtree.prefix} is reachable from {rev}"]
    squash_commit, split = squash
    if split != commit:
        return [f"{subtree.ledger} names {commit}, but the newest squash {squash_commit[:12]} is of {split}"]
    findings = check_files(repo / subtree.prefix, upstream_blobs(repo, squash_commit), subtree.prefix)
    if subtree.modification_notice:
        findings += check_notice(repo / subtree.prefix / "README.md", commit, subtree.prefix)
    if mirror:
        findings += check_mirror(repo, mirror, commit, squash_commit)
    return findings


def main(subtree: Subtree, description: str, argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=description)
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--rev", default="HEAD", help="history searched for the subtree squash")
    parser.add_argument("--mirror", help=f"the {subtree.mirror} mirror ref the ledger commit must be on")
    args = parser.parse_args(argv)
    try:
        findings = run(subtree, args.repo, args.rev, args.mirror)
    except GitError as exc:
        print(f"{subtree.name}: {exc}", file=sys.stderr)
        return 2
    for finding in findings:
        print(f"::error::{finding}" if "GITHUB_ACTIONS" in os.environ else finding)
    if findings:
        return 1
    checked = "license files, headers and modification notice" if subtree.modification_notice else "license files and headers"
    print(f"{subtree.prefix}: subtree, ledger, {checked} are consistent")
    return 0
