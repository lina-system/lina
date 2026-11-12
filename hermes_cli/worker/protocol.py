"""Worker wire contract and service identity; release versions are informational only."""

from __future__ import annotations

import contextlib
import fcntl  # windows-footgun: ok — worker services run on POSIX devices only
import os
import time
from collections.abc import Callable, Iterator
from dataclasses import dataclass
from pathlib import Path
from uuid import uuid4

from hermes_cli.worker.task import read_json, write_json

PROTOCOL_MIN = 1
PROTOCOL_MAX = 1
INSTANCE = "serve.instance.json"
# hello capability: ``probe`` takes the hub's runner spec as JSON on stdin (lina#337); a node without it is sent none
PROBE_SPEC = "probe_runner_spec"


@dataclass(frozen=True)
class Verb:
    handler: Callable[[Path, list[str], bytes], tuple[int, bytes]]
    effect: str
    stdin: bool = False
    legacy: bool = False


def verbs() -> dict[str, Verb]:
    """The only dispatch table, including payload and legacy recovery contracts."""
    import json
    from hermes_cli import lina_runner_probe as runner_probe
    from hermes_cli import lina_runner_sync_node as runner_sync
    from hermes_cli.worker import device as d

    return {
        "hello": Verb(lambda root, args, data: d._ok(hello(root)), "read"),
        "service": Verb(lambda root, args, data: d._ok(service_verb(root, args)), "destructive"),
        "put": Verb(lambda root, args, data: d._ok(d._put(root, args[0], args[1], data)), "write", stdin=True),
        "untar": Verb(lambda root, args, data: d._ok(d._untar(root, args[0], args[1], data)), "write", stdin=True),
        "meta": Verb(lambda root, args, data: d._ok(d.meta(root, args[0], live=args[1:] == ["live"])), "read", legacy=True),
        "probe": Verb(lambda root, args, data: d._ok(d.probe(root, *args[:2], hub_spec=d.hub_spec_of(data))), "read",
                       stdin=True),
        "reserve": Verb(lambda root, args, data: d._ok(d.slots.reserve(
            root, d.safe_id(args[0]), int(args[1]), pool=args[2] if len(args) > 2 else "bid")), "write"),
        "send": Verb(lambda root, args, data: d._ok(d.keyed(root, args, lambda: d.steer(
            root, args[0], data.decode("utf-8", "replace")))), "write", stdin=True),
        "follow_up": Verb(lambda root, args, data: d._ok(d.follow_up(root, args[0], json.loads(data))), "write", stdin=True),
        "human": Verb(lambda root, args, data: d._ok(d.human(root, args[0])), "read", legacy=True),
        "answer": Verb(lambda root, args, data: d._ok(d.keyed(root, args, lambda: d.answer(
            root, args[0], data.decode("utf-8", "replace")))), "write", stdin=True),
        "cancel": Verb(lambda root, args, data: d._ok(d.cancel(root, args[0], data.decode("utf-8", "replace"))),
                       "destructive", stdin=True, legacy=True),
        "pack": Verb(lambda root, args, data: (0, d.pack(root, args[0])), "read", legacy=True),
        "bundle": Verb(lambda root, args, data: d.bundle(root, *args[:2]), "read", legacy=True),
        "runner-sync": Verb(lambda root, args, data: d._ok(runner_sync.apply(root, json.loads(data))), "destructive",
                            stdin=True),
        "runner-probe": Verb(lambda root, args, data: d._ok(runner_probe.apply(root, json.loads(data))), "write",
                             stdin=True),
    }


@contextlib.contextmanager
def instance_lock(worker_root: Path) -> Iterator[None]:
    """Serialize instance publication with the compare-and-stop boundary."""
    worker_root.mkdir(parents=True, exist_ok=True)
    with (worker_root / "serve.instance.lock").open("a") as handle:
        fcntl.flock(handle, fcntl.LOCK_EX)  # windows-footgun: ok — POSIX worker services only
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)  # windows-footgun: ok — POSIX worker services only


def begin_instance(worker_root: Path) -> str:
    """Publish a new identity before this service writes its first heartbeat."""
    from hermes_cli.worker.device import HEARTBEAT

    instance_id = str(uuid4())
    with instance_lock(worker_root):
        (worker_root / HEARTBEAT).unlink(missing_ok=True)
        write_json(worker_root / INSTANCE, {"instance_id": instance_id, "pid": os.getpid(), "started_at": time.time()})
    return instance_id


def identity(worker_root: Path) -> tuple[bool, str | None]:
    """Whether serve is live and its instance; the compare-and-act boundary reads only this, never the runner probes."""
    from hermes_cli.worker import serve

    age = serve.heartbeat_age(worker_root)
    active = age is not None and age < 60
    record = read_json(worker_root / INSTANCE, {}) or {}
    return active, record.get("instance_id") if active else None


def hello(worker_root: Path) -> dict:
    """Describe this installation, its offered runners with their pins and readiness (lina#222, bounded by
    ``lina_runners.HELLO_TIMEOUT_S``), whether its model path answers (asked on the node, like a file node's hello),
    its OCX (``ocx``, as before) and its model proxy (``proxy``, ``device.proxy_report``, lina#586) and its supported
    access-mode vocabulary."""
    from hermes_cli import lina_runner_sync_node, lina_runners
    from hermes_cli.lina_work_settings import ACCESS
    from hermes_cli.lina_version import LINA_VERSION
    from hermes_cli.worker import device

    active, instance_id = identity(worker_root)
    ocx = lina_runner_sync_node.ocx_report(worker_root.parent)
    proxy = device.proxy_report(worker_root, ocx)
    return {"protocol": {"min": PROTOCOL_MIN, "max": PROTOCOL_MAX},
            "instance_id": instance_id, "serve": active,
            "version": LINA_VERSION,
            "capabilities": {"runners": lina_runners.cached_node_report(worker_root),
                             "platform": lina_runner_sync_node.platform(),
                             "ocx": ocx, **({"proxy": proxy} if proxy else {}),
                             **device.model_path(worker_root),
                             "access_modes": list(ACCESS), "verbs": list(verbs()),
                             "request_keys": True, PROBE_SPEC: True}}


def service_verb(worker_root: Path, args: list[str]) -> dict:
    """``service <ocx|worker> <action> [instance_id]`` through ``lina_runners.node_service`` (lina#222); the #218
    form ``service <stop|restart> <instance_id>`` still means the worker."""
    from hermes_cli import lina_runners

    named = bool(args) and args[0] in lina_runners.SERVICE_ACTIONS
    action, instance_id = (list(args[1:3] if named else args[:2]) + ["", ""])[:2]
    if named:
        return lina_runners.node_service(worker_root, args[0], action, instance_id)
    return service_action(worker_root, action, instance_id)


def service_action(worker_root: Path, action: str, instance_id: str) -> dict:
    """Stop only the compared instance; restart starts outside the publication lock."""
    from hermes_cli.worker import service

    if action not in ("stop", "restart"):
        return {"action": "error", "reason": "service expects stop or restart"}
    with instance_lock(worker_root):
        if not instance_id or identity(worker_root)[1] != instance_id:
            return {"action": "error", "reason": "worker instance changed: refresh hello", "stale_instance": True}
        lines: list[str] = []
        code = service.stop(worker_root.parent, out=lines.append)
    # Starting while holding the lock would deadlock the new serve's identity publication.
    if code == 0 and action == "restart":
        code = service.start(worker_root.parent, out=lines.append)
    if code:
        return {"action": "error", "reason": " ".join(lines) or f"service {action} failed"}
    return {"action": action, "instance_id": instance_id}
