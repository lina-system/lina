#!/usr/bin/env python3
"""Check the Honcho subtree in ``services/honcho`` against the upstream commit it came from.

``services/honcho`` is a squashed ``git subtree`` of plastic-labs/honcho (docs/honcho.md). The
squash commit git subtree writes holds Honcho's tree exactly as upstream has it, so it is the
reference this check compares the working tree with:

* the last row of ``services/honcho-intake.md`` names the upstream commit of the newest squash
  under ``services/honcho`` (its ``git-subtree-split`` trailer);
* every upstream LICENSE / COPYING / NOTICE file is still present and byte for byte unchanged;
* every copyright or SPDX header line of an upstream file is still at the top of that file;
* ``services/honcho/README.md`` starts with the LINA modification notice, and the notice names
  the ledger's upstream commit.

With ``--mirror REF`` (CI passes the fetched ``honcho-upstream`` branch) the ledger commit must
also be on the mirror and have the squash commit's tree, so the squash is upstream's own tree.

    python scripts/ci/check_honcho_subtree.py
    python scripts/ci/check_honcho_subtree.py --mirror origin/honcho-upstream

Exit codes: 0 = clean, 1 = at least one finding, 2 = git or usage error.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from pathlib import Path

PREFIX = "services/honcho"
LEDGER = "services/honcho-intake.md"
NOTICE_START = "<!-- lina-modification-notice:start -->"
NOTICE_END = "<!-- lina-modification-notice:end -->"
_SHA = re.compile(r"\b[0-9a-f]{40}\b")
_LICENSE_NAME = re.compile(r"^(LICENSE|LICENCE|COPYING|NOTICE)([.-].*)?$", re.IGNORECASE)
_HEADER_LINE = re.compile(r"copyright|spdx-license-identifier", re.IGNORECASE)
HEADER_LINES = 30  # how far from the top an upstream header line is looked for
HEADER_SLACK = 30  # extra lines LINA may put above it without it counting as moved


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


def newest_squash(repo: Path, rev: str) -> tuple[str, str] | None:
    out = _git(repo, "log", "--format=%H%x00%B%x01", f"--grep=^git-subtree-dir: {PREFIX}/*$", rev)
    for record in out.split("\x01"):
        if "\x00" not in record:
            continue
        commit, body = record.split("\x00", 1)
        if not re.search(rf"^git-subtree-dir: {re.escape(PREFIX)}/?$", body, re.MULTILINE):
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


def check_files(root: Path, upstream: dict[str, bytes]) -> list[str]:
    findings = []
    for path, content in sorted(upstream.items()):
        local = root / path
        is_license = bool(_LICENSE_NAME.match(Path(path).name))
        headers = header_lines(content, HEADER_LINES) if b"\0" not in content[:8000] else []
        if not is_license and not headers:
            continue
        if not local.is_file():
            findings.append(f"{PREFIX}/{path}: upstream {'license file' if is_license else 'file with a license header'}"
                            " is missing")
            continue
        current = local.read_bytes()
        if is_license and current != content:
            findings.append(f"{PREFIX}/{path}: license file differs from upstream")
        kept = set(header_lines(current, HEADER_LINES + HEADER_SLACK))
        for line in headers:
            if line not in kept:
                findings.append(f"{PREFIX}/{path}: upstream header line changed or removed: {line!r}")
    return findings


def check_notice(readme: Path, commit: str) -> list[str]:
    if not readme.is_file():
        return [f"{PREFIX}/README.md is missing"]
    text = readme.read_text(encoding="utf-8-sig", errors="replace")
    if not text.startswith(NOTICE_START) or NOTICE_END not in text:
        return [f"{PREFIX}/README.md must start with the LINA modification notice ({NOTICE_START} ... {NOTICE_END})"]
    block = text[:text.index(NOTICE_END)]
    if commit not in block:
        return [f"{PREFIX}/README.md: the modification notice does not name the ledger's upstream commit {commit}"]
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


def run(repo: Path, rev: str, mirror: str | None) -> list[str]:
    ledger = repo / LEDGER
    if not ledger.is_file():
        return [f"{LEDGER} is missing"]
    commit = ledger_commit(ledger.read_text(encoding="utf-8-sig"))
    if commit is None:
        return [f"{LEDGER} has no intake row with a full upstream commit sha"]
    squash = newest_squash(repo, rev)
    if squash is None:
        return [f"no git subtree squash for {PREFIX} is reachable from {rev}"]
    squash_commit, split = squash
    if split != commit:
        return [f"{LEDGER} names {commit}, but the newest squash {squash_commit[:12]} is of {split}"]
    findings = check_files(repo / PREFIX, upstream_blobs(repo, squash_commit))
    findings += check_notice(repo / PREFIX / "README.md", commit)
    if mirror:
        findings += check_mirror(repo, mirror, commit, squash_commit)
    return findings


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--repo", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--rev", default="HEAD", help="history searched for the subtree squash")
    parser.add_argument("--mirror", help="the honcho-upstream mirror ref the ledger commit must be on")
    args = parser.parse_args(argv)
    try:
        findings = run(args.repo, args.rev, args.mirror)
    except GitError as exc:
        print(f"check_honcho_subtree: {exc}", file=sys.stderr)
        return 2
    for finding in findings:
        print(f"::error::{finding}" if "GITHUB_ACTIONS" in os.environ else finding)
    if findings:
        return 1
    print(f"{PREFIX}: subtree, ledger, license files, headers and modification notice are consistent")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
