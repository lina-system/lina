"""``lina omo routing plan|apply|check`` (lina#34): ocx-omo (``tools/ocx-omo``) built and run on LINA's pinned Node.

ocx-omo reads the installed omo's built-in category and agent model order (``omo-task.js`` of
``vendor/omo/current``) and writes routing over the node's magpie models into ``~/.omo/omo.jsonc``, owning only the
routing keys of each route and backing the file up before it writes. Its arguments and exit codes pass through unchanged:
``check`` exits 0 when the routing is in step and 10 when there are changes to review.

The build copies the sources into ``vendor/ocx-omo/`` and runs ``npm ci`` + ``npm run build`` there with the
pinned Node, so the checkout stays clean; it is redone when the sources change (``.source-sha256``).

ocx-omo gets ``--remote-id <id>`` of the node's ``worker.proxy`` (lina#587), reads ``providers.magpie`` of omo's
catalog and writes ``magpie/<remote_id>/<provider>/<id>`` (``magpie/<provider>/<id>`` with ``remote_id: ""``, passed as
an empty ``--remote-id`` argument, lina#623).
"""

from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
from pathlib import Path
from typing import Callable

from hermes_cli import lina_deps, lina_omo
from hermes_cli.worker import proxy as proxies
from hermes_cli.worker.runners.base import RunnerError

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
        lina_deps.npm(root, node, "ci", "--ignore-scripts", cwd=staging, label="ocx-omo: npm ci", run=run)
        lina_deps.npm(root, node, "run", "build", cwd=staging, label="ocx-omo: npm run build", run=run)
        if not (staging / "dist" / "src" / "cli.js").exists():
            raise lina_deps.DepsError("ocx-omo: the build wrote no dist/src/cli.js")
        (staging / ".source-sha256").write_text(digest + "\n", encoding="utf-8")
        shutil.rmtree(dest, ignore_errors=True)
        os.replace(staging, dest)
    finally:
        shutil.rmtree(staging, ignore_errors=True)
    return cli_path(root)


def catalog_path() -> Path:
    return lina_omo.agent_dir() / "models.json"


def node_proxy(root: Path) -> proxies.Proxy:
    """The magpie of this node: ``worker.proxy`` of the LINA root's config."""
    from hermes_cli.worker import settings
    return proxies.of(settings.section(root))


def argv(root: Path, verb: str, args: list[str], proxy: proxies.Proxy = proxies.Proxy()) -> list[str]:
    """ocx-omo on the pinned Node, pointed at the pinned omo, omo's catalog and the node's magpie link unless the
    caller names others."""
    named = {arg.split("=", 1)[0] for arg in args}
    defaults = {"--omo-task": lina_omo.omo_task_path(root), "--models": catalog_path(), "--remote-id": proxy.remote_id}
    extra = [part for flag, path in defaults.items() if flag not in named for part in (flag, str(path))]
    return [str(lina_omo.node_bin(root)), str(cli_path(root)), verb, *args, *extra]


def catalog_problem() -> str | None:
    """Why omo's catalog cannot be routed over (None when it lists magpie's models): ocx-omo needs
    ``providers.magpie.models`` in omo's ``models.json``, written by magpie's connection command for omo."""
    path = catalog_path()
    missing = f"{path} has no providers.magpie models"
    try:
        models = json.loads(path.read_text(encoding="utf-8-sig"))["providers"]["magpie"]["models"]
    except FileNotFoundError:
        return f"{path} does not exist"
    except (OSError, ValueError, KeyError, TypeError):
        return missing
    return None if isinstance(models, list) and models else missing


def _missing(root: Path) -> str | None:
    if not cli_path(root).exists() or not lina_omo.node_bin(root).exists():
        return "ocx-omo is not built under the LINA home: run `lina omo install`"
    return None


def _argv(root: Path, verb: str, args: list[str]) -> tuple[list[str] | None, str | None]:
    """ocx-omo's command line for this node's proxy, else why it cannot run (not built, a wrong ``worker.proxy``)."""
    problem = _missing(root)
    if problem:
        return None, problem
    try:
        return argv(root, verb, args, node_proxy(root)), None
    except RunnerError as exc:
        return None, f"omo routing: {exc}"


def run_verb(verb: str, args: list[str], root: Path | None = None,
             run: Callable[..., subprocess.CompletedProcess] = subprocess.run, out=print) -> int:
    """Run ocx-omo with its output on this terminal; returns its exit code (2 when it is not built)."""
    root = root or lina_deps.lina_root()
    command, problem = _argv(root, verb, args)
    if problem:
        out(problem)
        return 2
    return run(command, env=lina_omo.omo_env(), check=False).returncode


def check(root: Path, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> tuple[int, str]:
    """``check`` with its output captured, for ``lina omo status``."""
    command, problem = _argv(root, "check", [])
    if problem:
        return 2, problem
    result = run(command, env=lina_omo.omo_env(), capture_output=True, text=True,
                 encoding="utf-8", errors="replace", timeout=120, check=False)
    return result.returncode, ((result.stdout or "") + (result.stderr or "")).strip()


def prepare(root: Path, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> str:
    """The route source's ``prepare`` (``lina_runner_routes``): ocx-omo built on the pinned Node."""
    return str(build(root, lina_omo.node_bin(root).parent.parent, run=run))


def apply(root: Path, run: Callable[..., subprocess.CompletedProcess] = subprocess.run) -> int:
    """The route source's ``apply`` with its output captured (the daily sync, lina#531)."""
    command, problem = _argv(root, "apply", [])
    if problem:
        return 2
    return run(command, env=lina_omo.omo_env(), capture_output=True, text=True, encoding="utf-8",
               errors="replace", timeout=120, check=False).returncode


def install_step(apply: bool = False, root: Path | None = None, out=print,
                 run: Callable[..., subprocess.CompletedProcess] = subprocess.run, **install_kw) -> int:
    """``lina omo install`` (and the worker step of ``lina install --role worker``): install the pinned omo, then
    ``routing check``; on pending changes print ``plan``, and write them only when *apply* is set."""
    root = root or lina_deps.lina_root()
    try:
        lina_omo.install(root, out=out, run=run, **install_kw)
    except lina_deps.DepsError as exc:
        out(f"omo: {exc}")
        return 1
    try:
        node_proxy(root)
    except RunnerError as exc:
        out(f"omo routing: {exc}; omo is installed, routing is unchanged")
        return 2
    problem = catalog_problem()
    if problem:
        out(f"omo routing: not set up: {problem} (run magpie's connection command for omo on this node); "
            "then run `lina omo install` again")
        return 0
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
