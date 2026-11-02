"""The device verbs over the SSH transport (lina#35, review of #128): every verb the hub sends with a payload reaches
the device entry point (``lina worker _device``) with that payload on stdin. A fake ``ssh`` runs the real entry point,
so the local transport, which calls ``device.dispatch`` directly, cannot hide a verb the entry point drops."""

import json
import os
import sys
from pathlib import Path

from hermes_cli.worker.settings import Device
from hermes_cli.worker.transport import SshTransport, put
from tests.hermes_cli import _worker_fakes as fakes

REPO = Path(__file__).resolve().parents[2]
TASK = "OT-SIO1102120050"


def _far(tmp_path, monkeypatch) -> tuple[SshTransport, Path]:
    base, bin_dir = tmp_path / "lina", tmp_path / "bin"
    fakes.write_config(base, "")
    bin_dir.mkdir()
    ssh = bin_dir / "ssh"
    ssh.write_text(f"#!{sys.executable}\nimport os, sys\nos.execvp('sh', ['sh', '-c', sys.argv[-1]])\n")
    ssh.chmod(0o755)
    monkeypatch.setenv("PATH", f"{bin_dir}{os.pathsep}{os.environ['PATH']}")
    lina = f"env PYTHONPATH={REPO} HERMES_HOME={base} {sys.executable} -m hermes_cli.main"
    return SshTransport(Device("far", "far-device", lina=lina)), base / "worker" / "tasks" / TASK


def test_a_follow_up_over_ssh_reaches_the_device_with_its_payload(tmp_path, monkeypatch):
    transport, task_dir = _far(tmp_path, monkeypatch)
    for name, text in (("prompt.1", "ulw first"), ("request.1", "{}"), ("taken.1", "1"), ("rc.1", "0")):
        put(transport, TASK, name, text)
    payload = {"prompt": "ulw add a line", "message": "add a line"}
    reply = transport.call(["follow_up", TASK], json.dumps(payload).encode("utf-8"))
    assert (reply.code, reply.json().get("action"), reply.json().get("run")) == (0, "requested", 2)
    assert (task_dir / "prompt.2").read_text() == "ulw add a line"
