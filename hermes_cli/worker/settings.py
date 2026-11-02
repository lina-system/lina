"""Where ``lina worker`` keeps its state, and what it reads from the LINA root's ``config.yaml``.

```yaml
devices:
  workers:                     # the work devices the hub bids over (`lina worker hosts`)
    build-a: {host: build-a, cap: 2, fast: true}      # ssh alias the hub reaches it by
    build-b: {host: build-b, cap: 1, no_repo: true}   # docs and light work only
    here:    {host: local, cap: 1, last: true}        # this machine; only when the others are full
worker:
  hub: {host: hub, lina: lina}     # how a device reaches the hub to report an end (ssh alias, lina command there)
  notice: {command: [my-notify, "{agent}"]}   # the end notice: body on stdin; unset = written to hub/notices only
  models: {...}                    # see models.py
  runners: {...}                   # see runners/__init__.py
```

Every value has a working default except the device list: without ``devices.workers`` nothing is bid on.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

LOCAL = "local"
DEFAULTS = {"cap": 1, "min_free_gb": 5, "unknown_after_s": 600, "input_max_mb": 300, "memory_max": "12G",
            "poll_s": 15.0, "stall_grace_s": 600.0, "answered_grace_s": 60.0, "background_max_s": 3 * 3600.0,
            "hard_timeout_s": 43200.0, "repo_url": "https://github.com/{repo}.git"}


def child_pythonpath() -> str:
    """``PYTHONPATH`` for the ``python -m hermes_cli.main`` children the worker starts: where ``hermes_cli`` is
    imported from, ahead of the caller's own path."""
    import os
    own = str(Path(__file__).resolve().parents[2])
    return os.pathsep.join([own, *[p for p in os.environ.get("PYTHONPATH", "").split(os.pathsep) if p and p != own]])


def root(base: Path | None = None) -> Path:
    from hermes_cli.lina_ocx import lina_root
    return (base or lina_root()) / "worker"


def tasks_dir(worker_root: Path) -> Path:
    return worker_root / "tasks"


def repos_dir(worker_root: Path) -> Path:
    return worker_root / "repos"


def hub_dir(worker_root: Path) -> Path:
    return worker_root / "hub"


def _config(base: Path | None = None) -> dict:
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    from hermes_cli.lina_ocx import lina_root
    path = (base or lina_root()) / "config.yaml"
    doc = read_config_doc_readonly(path) if config_exists(path) else None
    return doc if isinstance(doc, dict) else {}


def section(base: Path | None = None) -> dict:
    """``worker:`` of the LINA root's config (one worker serves every profile of that root)."""
    value = _config(base).get("worker")
    return value if isinstance(value, dict) else {}


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
    last: bool = False
    auto: bool = True
    lina: str = "lina"  # the lina command on that device

    @property
    def local(self) -> bool:
        return self.host == LOCAL


def _device(name: str, item: dict) -> Device | None:
    host = item.get("host")
    if not isinstance(host, str) or not host or item.get("off") is True:
        return None
    return Device(name, host, max(1, int(item.get("cap") or DEFAULTS["cap"])), item.get("fast") is True,
                  item.get("no_repo") is True, item.get("last") is True, item.get("auto") is not False,
                  str(item.get("lina") or "lina"))


def devices(base: Path | None = None, raw: dict | None = None) -> list[Device]:
    """``devices.workers`` in config order; entries that are off or carry no host are left out."""
    if raw is None:
        section_ = _config(base).get("devices")
        raw = section_.get("workers") if isinstance(section_, dict) else None
    if not isinstance(raw, dict):
        return []
    return [device for name, item in raw.items() if isinstance(item, dict) and (device := _device(str(name), item))]
