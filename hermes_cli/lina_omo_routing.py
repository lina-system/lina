"""``lina omo routing plan|apply|check`` (lina#34): ocx-omo (``tools/ocx-omo``) built and run on LINA's pinned Node.

ocx-omo reads the installed omo's built-in category and agent model order (``omo-task.js`` of
``vendor/omo/current``) and writes routing over OCX's models into ``~/.omo/omo.jsonc``, owning only the routing keys
of each route and backing the file up before it writes. Its arguments and exit codes pass through unchanged:
``check`` exits 0 when the routing is in step and 10 when there are changes to review.

The build copies the sources into ``vendor/ocx-omo/`` and runs ``npm ci`` + ``npm run build`` there with the
pinned Node, so the checkout stays clean; it is redone when the sources change (``.source-sha256``).
"""

from __future__ import annotations

import hashlib
import os
import shutil
import subprocess
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx, lina_omo

SOURCE = Path(__file__).resolve().parents[1] / "tools" / "ocx-omo"
_SOURCES = ("package.json", "package-lock.json", "tsconfig.json", "policy.default.json", "src", "test")
CHECK_STATES = {0: "in step", 10: "changes to review"}


def build_dir(root: Path) -> Path:
    return root / "vendor" / "ocx-omo"


def cli_path(root: Path) -> Path:
    return build_dir(root) / "dist" / "src" / "cli.js"


def _files(source: Path) -> list[Path]:
    found = []
    for name in _SOURCES:
        path = source / name
        found += sorted(p for p in path.rglob("*") if p.is_file()) if path.is_dir() else [path]
    return found


def source_digest(source: Path = SOURCE) -> str:
    digest = hashlib.sha256()
    for path in _files(source):
        digest.update(path.relative_to(source).as_posix().encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def build(root: Path, node: Path, source: Path = SOURCE,
          run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> Path:
    """The built ocx-omo CLI under ``vendor/ocx-omo``; rebuilt only when the sources changed."""
    dest, digest = build_dir(root), source_digest(source)
    stamp = dest / ".source-sha256"
    if cli_path(root).exists() and stamp.exists() and stamp.read_text(encoding="utf-8-sig").strip() == digest:
        return cli_path(root)
    staging = dest.with_name(".ocx-omo.partial")
    shutil.rmtree(staging, ignore_errors=True)
    staging.mkdir(parents=True)
    try:
        for path in _files(source):
            target = staging / path.relative_to(source)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
        lina_ocx.npm(root, node, "ci", "--ignore-scripts", cwd=staging, label="ocx-omo: npm ci", run=run)
        lina_ocx.npm(root, node, "run", "build", cwd=staging, label="ocx-omo: npm run build", run=run)
        if not (staging / "dist" / "src" / "cli.js").exists():
            raise lina_ocx.OcxError("ocx-omo: the build wrote no dist/src/cli.js")
        (staging / ".source-sha256").write_text(digest + "\n", encoding="utf-8")
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return cli_path(root)


def argv(root: Path, verb: str, args: list[str]) -> list[str]:
    """ocx-omo on the pinned Node, pointed at the pinned omo unless the caller names another ``--omo-task``."""
    extra = [] if any(arg.split("=", 1)[0] == "--omo-task" for arg in args) else [
        "--omo-task", str(lina_omo.omo_task_path(root))]
    return [str(lina_omo.node_bin(root)), str(cli_path(root)), verb, *args, *extra]


def _missing(root: Path) -> str | None:
    if not cli_path(root).exists() or not lina_omo.node_bin(root).exists():
        return "ocx-omo is not built under the LINA home: run `lina omo install`"
    return None


def run_verb(verb: str, args: list[str], root: Path | None = None,
             run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """Run ocx-omo with its output on this terminal; returns its exit code (2 when it is not built)."""
    root = root or lina_ocx.lina_root()
    problem = _missing(root)
    if problem:
        out(problem)
        return 2
    return run(argv(root, verb, args), env=lina_omo.omo_env(), check=False).returncode


def check(root: Path, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> tuple[int, str]:
    """``check`` with its output captured, for ``lina omo status``."""
    problem = _missing(root)
    if problem:
        return 2, problem
    result = run(argv(root, "check", []), env=lina_omo.omo_env(), capture_output=True, text=True,
                 encoding="utf-8", errors="replace", timeout=120, check=False)
    return result.returncode, ((result.stdout or "") + (result.stderr or "")).strip()


def install_step(apply: bool = False, root: Path | None = None, out=print,
                 run: Callable[..., subprocess.CompletedProcess] = subprocess.run, **install_kw) -> int:
    """``lina omo install`` (and the worker step of ``lina install --role worker``): install the pinned omo, then
    ``routing check``; on pending changes print ``plan``, and write them only when *apply* is set."""
    root = root or lina_ocx.lina_root()
    try:
        lina_omo.install(root, out=out, run=run, **install_kw)
    except lina_ocx.OcxError as exc:
        out(f"omo: {exc}")
        return 1
    code = run_verb("check", [], root, run=run, out=out)
    if code not in CHECK_STATES:
        out(f"omo routing check failed (exit {code}); omo is installed, routing is unchanged")
        return code
    if code == 0:
        return 0
    if not apply:
        out("What `lina omo routing apply` would change:")
        run_verb("plan", [], root, run=run, out=out)
        out("Routing is not applied: review it, then run `lina omo routing apply` (or `lina omo install --apply`)")
        return 0
    return run_verb("apply", [], root, run=run, out=out)
