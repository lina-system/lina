"""Where ``lina worker`` keeps its state, and what it reads from the LINA root's ``config.yaml``.

```yaml
devices:
  workers:                     # the work devices the hub bids over (`lina worker hosts`)
    build-a: {host: build-a, cap: 2, fast: true}      # ssh alias the hub reaches it by
    build-b: {host: build-b, cap: 1, no_repo: true}   # docs and light work only
    here:    {host: local, cap: 1, named_cap: 2, last: 2}  # tier 2 fallback; separate --host capacity
    files:   {host: files, kind: file, role: files, omo: "~/.bun/bin/omo",   # a file node (docs/file-node.md)
              deny: [/volume1/backup, "#recycle", "#snapshot", "@*"], volumes: [/volume1]}
    phone:   {host: phone, role: person}                # a person's device: never takes work
worker:
  hub: {host: hub, lina: lina}     # how a device reaches the hub to report an end (ssh alias, lina command there)
  notice: {command: [my-notify, "{agent}"]}   # the end notice: body on stdin; unset = written to hub/notices only
  card_link: {kind: hermes, command: [/path/to/hermes]}  # Hermes cards: the Hermes Agent CLI by absolute path (kanban_link.py)
  todo_link: {kind: hermes, command: [my-todo]}  # `<command> link|evidence|note <to-do> ...` for --todo; unset = none
  hub_rules: "- This machine is the hub: ..."     # added to the first prompt of a run on a `host: local` device
  models: {...}                    # see models.py
  proxy: {remote_id: hub}          # this node's magpie and its link to the hub: proxy.py, docs/worker-proxy-switch.md
  runners: {...}                   # see runners/__init__.py
```

Every value has a working default except the device list: without ``devices.workers`` nothing is bid on.

``role`` is ``work`` (bid on; the default), ``files`` (only by ``--host``, never with a repository) or ``person``
(never takes work). ``kind: file`` drives the device over ssh without LINA on it (``file_node.py``); its role
defaults to ``files``.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

LOCAL = "local"
ROLES = ("work", "files", "person")
FILE_KIND = "file"
MODEL_CHECK = "http://127.0.0.1:3425/v1/models"  # a node's model path: its own magpie's model list, asked on the node
DEFAULTS = {"cap": 1, "min_free_gb": 5, "min_free_mem_gb": 4, "input_max_mb": 300, "memory_max": "12G",
            "cooloff_failures": 2, "cooloff_window_s": 1800, "cooloff_quick_s": 120, "cooloff_s": 900,
            "poll_s": 15.0, "stall_grace_s": 600.0, "answered_grace_s": 60.0, "background_max_s": 3 * 3600.0,
            "human_max_s": 3 * 3600.0, "hard_timeout_s": 43200.0, "repo_url": "https://github.com/{repo}.git"}


def child_pythonpath() -> str:
    """``PYTHONPATH`` for the ``python -m hermes_cli.main`` children the worker starts: where ``hermes_cli`` is
    imported from, ahead of the caller's own path."""
    import os
    own = str(Path(__file__).resolve().parents[2])
    return os.pathsep.join([own, *[p for p in os.environ.get("PYTHONPATH", "").split(os.pathsep) if p and p != own]])


def lina_home() -> Path:
    """LINA's root for the worker's state: ``LINA_HOME`` when set, whatever ``HERMES_HOME`` says. Without it, a
    ``HERMES_HOME`` of Hermes Agent's (a Hermes card session's) is ignored for the platform default; any other is
    how a LINA process hands its root to its children (services, devices over ssh, shims). ``HERMES_KANBAN_*`` is
    never read."""
    import os
    from hermes_constants import get_default_hermes_root, is_hermes_agent_path, platform_default_home_for
    hermes_home = os.environ.get("HERMES_HOME", "").strip()
    if os.environ.get("LINA_HOME", "").strip() or not hermes_home or is_hermes_agent_path(hermes_home):
        return platform_default_home_for(os.environ, Path.home())
    return get_default_hermes_root()


CALLER_HERMES_HOME = "LINA_CALLER_HERMES_HOME"  # the caller's Hermes Agent HERMES_HOME, kept by LINA's bootstrap


def caller_hermes_home() -> Path | None:
    """The Hermes Agent ``HERMES_HOME`` the caller ran under: the one LINA's bootstrap kept when it dropped it
    (``hermes_bootstrap._drop_foreign_home``), else a ``HERMES_HOME`` under Hermes Agent's home still in this
    process."""
    import os
    from hermes_constants import is_hermes_agent_path
    kept = os.environ.get(CALLER_HERMES_HOME, "").strip()
    if kept:
        return Path(kept).expanduser()
    home = os.environ.get("HERMES_HOME", "").strip()
    return Path(home).expanduser() if home and is_hermes_agent_path(home) else None


def hub_rules(worker: dict) -> str:
    """``worker.hub_rules``: the text added to the first prompt of a run on the hub node; "" when unset."""
    value = worker.get("hub_rules")
    return value.strip() if isinstance(value, str) else ""


def todo_link(worker: dict) -> list[str] | None:
    """``worker.todo_link: {kind: hermes, command: [...]}`` as its argv; None when unset. ValueError when it is
    malformed."""
    link = worker.get("todo_link")
    if link is None:
        return None
    command = link.get("command") if isinstance(link, dict) and link.get("kind") == "hermes" else None
    if not isinstance(command, list) or not command or not all(isinstance(part, str) and part for part in command):
        raise ValueError("worker.todo_link must be {kind: hermes, command: [<argv>]}")
    return command


def root(base: Path | None = None) -> Path:
    return (base or lina_home()) / "worker"


def tasks_dir(worker_root: Path) -> Path:
    return worker_root / "tasks"


def repos_dir(worker_root: Path) -> Path:
    return worker_root / "repos"


def hub_dir(worker_root: Path) -> Path:
    return worker_root / "hub"


def _config(base: Path | None = None) -> dict:
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    path = (base or lina_home()) / "config.yaml"
    doc = read_config_doc_readonly(path) if config_exists(path) else None
    return doc if isinstance(doc, dict) else {}


def section(base: Path | None = None) -> dict:
    """``worker:`` of the LINA root's config (one worker serves every profile of that root)."""
    value = _config(base).get("worker")
    return value if isinstance(value, dict) else {}


def legacy_omo_task(base: Path | None = None) -> str | None:
    """``worker.legacy_omo_task``: the old tool that answers for runs it started before the switch to
    ``lina worker`` (a card parked on them still wakes after the later end, lina#390); None when unset."""
    value = section(base).get("legacy_omo_task")
    return str(Path(value).expanduser()) if isinstance(value, str) and value.strip() else None


def setting(name: str, base: Path | None = None, worker: dict | None = None):
    worker = section(base) if worker is None else worker
    value = worker.get(name)
    return DEFAULTS.get(name) if value is None else value


@dataclass(frozen=True)
class Device:
    name: str
    host: str  # "local" or an ssh alias
    cap: int = 1
    fast: bool = False
    no_repo: bool = False
    last: int = 0
    auto: bool = True
    lina: str = "lina"  # the lina command on that device
    named_cap: int | None = None  # independent --host capacity; defaults to cap
    role: str = "work"  # work | files | person (ROLES); anything else takes no work
    kind: str = "lina"  # "file": a file node driven over ssh (file_node.py)
    omo: str = "omo"  # a file node's omo command
    deny: tuple[str, ...] = ()  # a file node's denied folders: absolute paths and folder-name patterns
    volumes: tuple[str, ...] = ()  # where a file node's folder-name patterns are looked for (3 levels deep)
    root: str = "lina-file-node"  # a file node's task folders, under its HOME
    model_check: str | None = None  # the node's model path, asked on the node; None: MODEL_CHECK
    scan_s: int = 300  # how long the denied-folder check may look after a run

    @property
    def manual_cap(self) -> int:
        return self.cap if self.named_cap is None else self.named_cap

    @property
    def local(self) -> bool:
        return self.host == LOCAL


def _device(name: str, item: dict) -> Device | None:
    host = item.get("host")
    if not isinstance(host, str) or not host or item.get("off") is True:
        return None
    kind = str(item.get("kind") or "lina")
    file_node = {}
    if kind == FILE_KIND:
        file_node = {"omo": str(item.get("omo") or "omo"), "deny": _strings(item.get("deny")),
                     "volumes": _strings(item.get("volumes")), "root": str(item.get("root") or "lina-file-node"),
                     "scan_s": max(1, int(item.get("scan_s") or Device.scan_s))}
    return Device(name, host, max(1, int(item.get("cap") or DEFAULTS["cap"])), item.get("fast") is True,
                  item.get("no_repo") is True, max(0, int(item.get("last") or 0)), item.get("auto") is not False,
                  str(item.get("lina") or "lina"),
                  max(1, int(item["named_cap"])) if item.get("named_cap") is not None else None,
                  str(item.get("role") or ("files" if kind == FILE_KIND else "work")), kind,
                  model_check=str(item["model_check"]) if item.get("model_check") else None, **file_node)


def _strings(value) -> tuple[str, ...]:
    return tuple(str(each) for each in value if str(each).strip()) if isinstance(value, list) else ()


def devices(base: Path | None = None, raw: dict | None = None) -> list[Device]:
    """``devices.workers`` in config order; entries that are off or carry no host are left out."""
    if raw is None:
        section_ = _config(base).get("devices")
        raw = section_.get("workers") if isinstance(section_, dict) else None
    if not isinstance(raw, dict):
        return []
    return [device for name, item in raw.items() if isinstance(item, dict) and (device := _device(str(name), item))]
