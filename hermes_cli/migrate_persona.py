"""Read legacy personas and publish a reviewed split into a destination install."""

from __future__ import annotations

import json
import re
from argparse import Namespace
from html import escape
from io import StringIO
from pathlib import Path
from stat import S_ISREG

from hermes_cli.migrate_memory_copy import _safe
from hermes_cli.persona_split import LedgerRow, Occurrence, split_personas
from hermes_constants import COMMON_PERSONA_FILE, PROFILE_ID_RE, WORK_PERSONA_FILE, assert_named_profile_home_live
from utils import atomic_write_bytes


def _claim_file(path: Path, seen: set[Path | tuple[int, int]]) -> bool:
    """Reserve a path and, when it exists, its physical file identity."""
    _safe(path)
    if path in seen:
        raise ValueError(f"file path supplied more than once: {path}")
    seen.add(path)
    try:
        info = path.stat()
    except FileNotFoundError:
        return False
    if not S_ISREG(info.st_mode):
        raise ValueError(f"expected a regular file: {path}")
    identity = (info.st_dev, info.st_ino)
    if identity in seen:
        raise ValueError(f"file aliases another migration path: {path}")
    seen.add(identity)
    return True


def _input_path(value: str, root: Path, seen: set[Path | tuple[int, int]]) -> Path:
    path = Path(value).expanduser().absolute()
    if path.is_relative_to(root) or not _claim_file(path, seen):
        raise ValueError(f"expected a regular input file outside the destination: {path}")
    return path


def _sources(specs: list[str], root: Path, seen: set[Path | tuple[int, int]]) -> dict[str, str]:
    sources = {}
    for spec in specs:
        name, sep, value = spec.partition("=")
        if not sep or not PROFILE_ID_RE.fullmatch(name) or name in sources:
            raise ValueError("--source requires a unique profile name followed by =FILE")
        path = _input_path(value, root, seen)
        sources[name] = path.read_bytes().decode("utf-8")
    return sources


def _drops(value: str | None, root: Path, seen: set[Path | tuple[int, int]]) -> dict[str, str]:
    drops = {}
    if value:
        path = _input_path(value, root, seen)
        for number, line in enumerate(path.read_text(encoding="utf-8-sig").splitlines(), 1):
            if not line.strip():
                continue
            digest, sep, reason = line.partition(":")
            if not sep or not re.fullmatch("[0-9a-f]{64}", digest) or not reason.strip() or digest in drops:
                raise ValueError(f"invalid or repeated sha256:reason at drop file line {number}")
            drops[digest] = reason.strip()
    return drops


def _json_line(value) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True) + "\n"


def _pair_report(sources: dict[str, str], ledger: list[LedgerRow]) -> bytes:
    """Render the exact source spans side by side in a Markdown HTML table."""
    replacements = [row for row in ledger if row["disposition"] == "replaced"]
    if not replacements:
        return b"# Persona replacement pairs\n\nNo replacements.\n"
    lines = {home: list(StringIO(text, newline="")) for home, text in sources.items()}
    parts = [
        "# Persona replacement pairs\n\n",
        '<table>\n<thead><tr><th scope="col">Original</th><th scope="col">Canonical</th>'
        '<th scope="col">Similarity</th></tr></thead>\n<tbody>\n',
    ]
    for row in replacements:
        parts.append("<tr>\n")
        pair: tuple[Occurrence, Occurrence] = (row, row["canonical"])
        for occurrence in pair:
            home = occurrence["home"]
            start = occurrence["location"]["start_line"]
            end = occurrence["location"]["end_line"]
            raw = "".join(lines[home][start - 1:end])
            # Keep source line breaks from terminating the Markdown HTML block.
            text = escape(raw).replace("\r", "&#13;").replace("\n", "&#10;")
            parts.append(
                f"<td><p>{escape(home)}, lines {start}–{end}</p>"
                f"<pre><code>{text}</code></pre></td>\n"
            )
        parts.append(f'<td>{row["ratio"]}</td>\n</tr>\n')
    parts.append("</tbody>\n</table>\n")
    return "".join(parts).encode("utf-8")


def _migrate(args: Namespace) -> dict:
    root = _safe(Path(args.home).expanduser())
    seen: set[Path | tuple[int, int]] = set()
    sources = _sources(args.source, root, seen)
    split = split_personas(sources, _drops(args.drop, root, seen), exempt=frozenset(args.exempt))
    # Preflight every output before replacing even an unedited destination copy.
    # A completed or interrupted apply can be repeated with the same inputs.
    outputs: dict[Path, bytes | None] = {}
    originals: dict[Path, bytes] = {}
    for name, persona in split.personas.items():
        home = root if name == "default" else root / "profiles" / name
        outputs[home / COMMON_PERSONA_FILE] = split.common[name].encode() or None
        outputs[home / WORK_PERSONA_FILE] = split.work[name].encode() or None
        path = home / "SOUL.md"
        originals[path] = sources[name].encode()
        outputs[path] = persona.encode()
        rows = (_json_line(row) for row in split.ledger if row["home"] == name)
        outputs[home / "persona-ledger.jsonl"] = "".join(rows).encode()

    report_path = None
    if args.report_pairs:
        report_path = _safe(Path(args.report_pairs).expanduser())
        if any(report_path.is_relative_to(path) or path.is_relative_to(report_path) for path in outputs):
            raise ValueError(f"pair report path conflicts with a planned output: {report_path}")
        outputs[report_path] = _pair_report(sources, split.ledger)

    pending: dict[Path, bytes] = {}
    for path, data in outputs.items():
        if _claim_file(path, seen):
            current = path.read_bytes()
            if current == (data or b""):
                continue
            if current != originals.get(path):
                raise ValueError(f"destination contains different data: {path}")
        if data is not None:
            if args.apply or path == report_path:
                assert_named_profile_home_live(path.parent)
            pending[path] = data
    for path, data in pending.items():
        if args.apply or path == report_path:
            atomic_write_bytes(path, data, mode=0o600)
    return {"ok": True, "applied": args.apply, "summary": split.summary, "ledger": split.ledger}


def run(args: Namespace) -> int:
    try:
        report = _migrate(args)
    except (OSError, ValueError, RuntimeError) as exc:
        report = {"ok": False, "error": str(exc)}
    print(json.dumps(report, ensure_ascii=False))
    return 0 if report["ok"] else 1
