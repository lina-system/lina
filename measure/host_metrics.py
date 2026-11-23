"""Read Linux process counters and identify who opened the model socket."""

import json
import os
import socket
from dataclasses import dataclass
from pathlib import Path
from typing import Final

PROC: Final = Path("/proc")
TICKS: Final = os.sysconf("SC_CLK_TCK")


class MeasurementError(RuntimeError):
    """The run could not prove the selected measurement."""


@dataclass(frozen=True, slots=True)
class Process:
    pid: int
    ppid: int
    start_ticks: int
    user_ticks: int
    sys_ticks: int

    @classmethod
    def read(cls, pid: int) -> "Process":
        fields = (PROC / str(pid) / "stat").read_text().rsplit(")", 1)[1].split()
        return cls(pid, int(fields[1]), int(fields[19]), int(fields[11]), int(fields[12]))

    def same_process(self, other: "Process") -> bool:
        return (self.pid, self.start_ticks) == (other.pid, other.start_ticks)

    def cpu_ms(self, before: "Process | None") -> dict[str, float]:
        if before is not None and not self.same_process(before):
            raise MeasurementError(f"PID {self.pid} changed identity during the sample")
        return {
            "user_ms": (self.user_ticks - (before.user_ticks if before else 0)) * 1000 / TICKS,
            "sys_ms": (self.sys_ticks - (before.sys_ticks if before else 0)) * 1000 / TICKS,
        }

    def pss_kib(self) -> int:
        for line in (PROC / str(self.pid) / "smaps_rollup").read_text().splitlines():
            if line.startswith("Pss:"):
                return int(line.split()[1])
        raise MeasurementError(f"PID {self.pid} has no PSS in smaps_rollup")

    def identity(self) -> dict[str, int | str]:
        directory = PROC / str(self.pid)
        argv = (directory / "cmdline").read_bytes().split(b"\0")
        return {
            "pid": self.pid,
            "ppid": self.ppid,
            "start_ticks": self.start_ticks,
            "exe": os.readlink(directory / "exe"),
            "entry": " ".join(os.fsdecode(arg) for arg in argv[:3] if arg),
        }


def registered_host(home: Path, sid: str, impl: str) -> Process | None:
    """Resolve only this synthetic home's compute host registry."""
    if impl == "inproc":
        return None
    filename = (
        f"dashboard-rust-host-{sid}.json" if impl == "rust" else "dashboard-compute-host.json"
    )
    try:
        registry = json.loads((home / "state" / filename).read_text())
    except FileNotFoundError:
        return None
    return Process.read(int(registry["host_pid"]))


def model_socket_inode(peer_port: int, fixture_port: int) -> str:
    """Look up the client half of the accepted 127.0.0.1 TCP connection."""
    addr = socket.inet_aton("127.0.0.1")[::-1].hex().upper()
    endpoints = [f"{addr}:{port:04X}" for port in (peer_port, fixture_port)]
    for line in (PROC / "net" / "tcp").read_text().splitlines()[1:]:
        fields = line.split()
        if fields[1:3] == endpoints and fields[3] == "01":
            return fields[9]
    raise MeasurementError(f"Could not find live model client socket on port {peer_port}")


def require_socket_owner(process: Process, inode: str) -> None:
    """Prove the selected process holds the model request's client socket."""
    for fd in (PROC / str(process.pid) / "fd").iterdir():
        try:
            if os.readlink(fd) == f"socket:[{inode}]":
                return
        except FileNotFoundError:
            continue  # An unrelated descriptor closed while listing it.
    raise MeasurementError(f"PID {process.pid} does not own model client socket inode {inode}")
