"""Hub ↔ device: send files, run a command, fetch files. ssh today; a Node channel can replace it behind ``Transport``.

Every call is one ``lina worker _device <verb> …`` on the device with bytes on stdin and one JSON line (or a tar for
``pack``) on stdout, so the device resolves its own LINA home and the hub never builds remote paths or shell
scripts. ``LocalTransport`` runs the same verbs in this process, for a device that is the hub's own machine.
"""

from __future__ import annotations

import io
import json
import shlex
import shutil
import subprocess
import tarfile
from dataclasses import dataclass
from pathlib import Path
from typing import Protocol

from hermes_cli.worker.settings import Device


class TransportError(RuntimeError):
    pass


@dataclass
class Reply:
    code: int
    out: bytes
    err: str = ""

    def json(self) -> dict:
        lines = self.out.decode("utf-8", "replace").strip().splitlines()
        try:
            value = json.loads(lines[-1]) if lines else {}
        except ValueError:
            value = {}
        return value if isinstance(value, dict) else {}


class Transport(Protocol):
    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply: ...


def put(transport: Transport, task_id: str, rel: str, data: bytes | str) -> None:
    payload = data.encode("utf-8") if isinstance(data, str) else data
    reply = transport.call(["put", task_id, rel], payload, timeout=120)
    if reply.code != 0:
        raise TransportError(f"put {rel}: {reply.err.strip()[-300:] or reply.json().get('reason')}")


def put_tree(transport: Transport, task_id: str, rel: str, sources: list[Path]) -> None:
    """Copy files and folders into ``<task>/<rel>/`` on the device, as one tar."""
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for source in sources:
            archive.add(str(source), arcname=source.name)
    reply = transport.call(["untar", task_id, rel], buffer.getvalue(), timeout=600)
    if reply.code != 0:
        raise TransportError(f"copy into {rel}: {reply.err.strip()[-300:] or reply.json().get('reason')}")


# Archive name -> task-relative source. Fetch clears these same names before extraction.
PACKED = {"report.md": "work/report.md", "final.md": "final.md", "manifest.json": "manifest.json",
          "git.txt": "git.txt", "stderr.txt": "stderr.{run}.txt", "out": "work/out"}


def fetch(transport: Transport, task_id: str, dest: Path) -> list[str]:
    """The run's results (``report.md``, ``final.md``, ``manifest.json``, ``git.txt``, ``out/``) into *dest*."""
    reply = transport.call(["pack", task_id], timeout=600)
    if reply.code != 0:
        raise TransportError(f"fetch {task_id}: {reply.err.strip()[-300:]}")
    dest.mkdir(parents=True, exist_ok=True)
    for name in PACKED:  # a deliverable the new run no longer has must not survive from an earlier fetch
        stale = dest / name
        if stale.is_dir() and not stale.is_symlink():
            shutil.rmtree(stale)
        else:
            stale.unlink(missing_ok=True)
    with tarfile.open(fileobj=io.BytesIO(reply.out), mode="r") as archive:
        archive.extractall(dest, filter="data")
        return archive.getnames()


def fetch_bundle(transport: Transport, task_id: str, dest: Path, ref: str = "HEAD") -> Path:
    """The task's work-folder branch as a git bundle (``_device bundle``) written to *dest*."""
    reply = transport.call(["bundle", task_id, ref], timeout=600)
    if reply.code != 0 or not reply.out.startswith(b"# v"):
        raise TransportError(f"bundle {task_id}: {reply.err.strip()[-300:] or reply.json().get('reason') or 'no bundle'}")
    dest.write_bytes(reply.out)
    return dest


class SshTransport:
    def __init__(self, device: Device, connect_timeout: int = 10):
        self.device, self.connect_timeout = device, connect_timeout

    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply:
        remote = shlex.join([*shlex.split(self.device.lina), "worker", "_device", *verb])
        argv = ["ssh", "-o", "BatchMode=yes", "-o", f"ConnectTimeout={self.connect_timeout}", self.device.host, remote]
        try:
            done = subprocess.run(argv, input=stdin, capture_output=True, timeout=timeout, check=False)
        except subprocess.TimeoutExpired:
            return Reply(124, b"", f"{self.device.name}: no answer in {timeout:.0f}s")
        except OSError as exc:
            return Reply(255, b"", str(exc))
        return Reply(done.returncode, done.stdout, done.stderr.decode("utf-8", "replace"))


class LocalTransport:
    def __init__(self, worker_root: Path):
        self.worker_root = worker_root

    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply:
        from hermes_cli.worker import device
        code, out = device.dispatch(self.worker_root, verb, stdin)
        return Reply(code, out)


class NegotiatedTransport:
    """One contact's descriptor; never persisted or shared across hub ticks."""

    def __init__(self, raw: Transport, name: str):
        self.raw, self.name = raw, name
        self.descriptor: dict | None = None
        self._hello: Reply | None = None
        self._reason = ""
        self._legacy = False

    @property
    def reason(self) -> str:
        """Why this contact refuses calls (its hello failed or the protocols do not meet); "" when it takes them."""
        return self._reason

    def _refused(self, reason: str) -> Reply:
        code = self._hello.code if self._hello and not self._legacy and self._hello.code else 1
        return Reply(code, json.dumps({"action": "error", "reason": reason, "protocol_error": True}).encode(), reason)

    def refresh(self, timeout: float = 120) -> Reply:
        from hermes_cli.worker import protocol

        reply = self.raw.call(["hello"], timeout=timeout)
        self._hello, self.descriptor = reply, reply.json()
        self._legacy = reply.code != 0 and self.descriptor.get("reason") == "unknown verb hello"
        self._reason = ""
        if self._legacy:
            self._reason = f"{self.name} needs update: worker hello is unavailable"
        elif reply.code != 0:
            self._reason = f"{self.name}: worker hello failed: {' '.join(reply.err.split()) or f'exit {reply.code}'}"
        else:
            bounds = self.descriptor.get("protocol")
            if (not isinstance(bounds, dict) or type(bounds.get("min")) is not int
                    or type(bounds.get("max")) is not int or not 0 <= bounds["min"] <= bounds["max"]):
                self._reason = f"{self.name}: invalid worker protocol range"
            elif bounds["min"] > protocol.PROTOCOL_MAX or bounds["max"] < protocol.PROTOCOL_MIN:
                side = "this hub" if bounds["min"] > protocol.PROTOCOL_MAX else self.name
                self._reason = (f"{self.name} speaks worker protocol {bounds['min']}-{bounds['max']}, "
                                f"this hub {protocol.PROTOCOL_MIN}-{protocol.PROTOCOL_MAX}: update {side}")
        return reply

    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply:
        from hermes_cli.worker.protocol import verbs

        if verb[0] == "hello":
            return self.refresh(timeout)
        if self._hello is None:
            self.refresh(timeout)
        info = verbs().get(verb[0])
        if self._reason and not (self._legacy and info and info.legacy):
            return self._refused(self._reason)
        return self.raw.call(verb, stdin, timeout)

    def service(self, action: str, recorded_instance_id: str) -> Reply:
        """Recheck the recorded instance, then let the node compare-and-act; never retry a stale action."""
        self.refresh()
        if self._reason:
            return self._refused(self._reason)
        if not recorded_instance_id or self.descriptor.get("instance_id") != recorded_instance_id:
            return self._refused(f"{self.name}: worker instance changed: refresh hello")
        reply = self.call(["service", action, recorded_instance_id])
        if reply.json().get("stale_instance"):
            self.refresh()
        return reply


def for_device(dev: Device, worker_root: Path) -> NegotiatedTransport:
    """In this process for the hub's own machine, the hub-run verbs for a file node (``kind: file``), else ssh."""
    from hermes_cli.worker.settings import FILE_KIND
    if dev.local:
        raw: Transport = LocalTransport(worker_root)
    elif dev.kind == FILE_KIND:
        from hermes_cli.worker.file_node import FileNodeTransport
        raw = FileNodeTransport(dev, worker_root)
    else:
        raw = SshTransport(dev)
    return NegotiatedTransport(raw, dev.name)


def notify_hub(notify: dict, task_id: str, state: str, run: int, worker_root: Path) -> bool:
    """The device's end report: ``lina worker _notify <ID> <state> <run>`` on the hub (in-process when local)."""
    if notify.get("via") == "local":
        from hermes_cli.worker import hub
        hub.notify(worker_root, task_id, state, str(run))
        return True
    host, lina = notify.get("host"), notify.get("lina") or "lina"
    if not host:
        return False
    remote = shlex.join([*shlex.split(lina), "worker", "_notify", task_id, state, str(run)])
    try:
        done = subprocess.run(["ssh", "-n", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, remote],
                              capture_output=True, timeout=120, check=False)
    except (OSError, subprocess.TimeoutExpired):
        return False
    return done.returncode == 0
