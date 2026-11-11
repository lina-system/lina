"""The real-call probe per device and runner (lina#531): the ``runner-probe`` verb runs a block's probe against the
version the node runs and records ``done`` or ``failed`` with a one-line cause; that record is what the runner row
(``lina devices``, ``hello``) and the bidding probe (``lina worker hosts``) show as ready, after the static checks.

The tool is a fake binary added by a settings block; its answer is steered by ``FAKETOOL_ANSWER``.
"""
import hashlib
import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_ocx, lina_runner_probe, lina_runners
from hermes_cli.worker import device, slots

TOOL = (f"#!{sys.executable}\nimport os, sys\nif '--version' in sys.argv:\n    print('faketool 3.1.0')\n"
        "    sys.exit(0)\nprint(os.environ.get('FAKETOOL_ANSWER', 'OK'))\n").encode()
URL = "https://fake.invalid/faketool"


def fetch(url: str, dest: Path) -> None:
    assert url == URL
    Path(dest).write_bytes(TOOL)


@pytest.fixture
def node(tmp_path, monkeypatch):
    account = tmp_path / "acct"
    root = account / ".lina"
    root.mkdir(parents=True)
    monkeypatch.setenv("HOME", str(account))
    monkeypatch.delenv("FAKETOOL_ANSWER", raising=False)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    shim = lina_runners.shim_path(root, "faketool")
    block = {"type": "command", "argv": [str(shim), "-p"],
             "install": {"installer": "binary", "version": "3.1.0", "bin": "faketool", "entry": "faketool",
                         "artifacts": {lina_ocx.platform_key(): {"url": URL,
                                                                  "sha256": hashlib.sha256(TOOL).hexdigest()}}},
             "sign_in": "none", "probe": {"argv": ["-p", "{prompt}"]}}
    (root / "config.yaml").write_text(json.dumps({"worker": {"default_runner": "faketool",
                                                             "runners": {"faketool": block}}}))
    assert lina_runners.install("faketool", root, fetch=fetch, out=lambda line: None) == 0
    return root


def _probe(root: Path) -> dict:
    return lina_runner_probe.apply(root / "worker", {"runners": {"faketool": {}}})


@pytest.mark.platforms("posix")
def test_a_passing_probe_is_recorded_and_the_row_stays_ready(node):
    result = _probe(node)["results"]["faketool"]
    assert (result["state"], result["why"], result["version"]) == ("done", "answered OK", "3.1.0")
    assert lina_runner_probe.results(node)["faketool"] == result
    row = lina_runners.row(node, lina_runners.pins()["faketool"])
    assert row["ready"] is True and row["probe"]["state"] == "done"
    assert "probe done" in lina_runners.render([{**row, "name": "faketool"}])[0]
    assert device.probe(node / "worker", "", "faketool")["ready"] is True


@pytest.mark.platforms("posix")
def test_a_failed_probe_takes_the_runner_out_of_ready_on_devices_and_in_the_bid(node, monkeypatch):
    monkeypatch.setenv("FAKETOOL_ANSWER", "Authentication required")
    result = _probe(node)["results"]["faketool"]
    assert (result["state"], result["why"]) == ("failed", "did not answer OK: Authentication required")

    row = lina_runners.row(node, lina_runners.pins()["faketool"])
    assert (row["ready"], row["why"]) == (False, "probe failed"), "static checks pass, the real call decides"
    assert "probe failed (did not answer OK: Authentication required)" in \
        lina_runners.render([{**row, "name": "faketool"}])[0]
    found = device.probe(node / "worker", "", "faketool")
    assert found["ready"] is False and "last probe failed (did not answer OK" in found["why"]

    monkeypatch.delenv("FAKETOOL_ANSWER")
    assert _probe(node)["results"]["faketool"]["state"] == "done"
    assert device.probe(node / "worker", "", "faketool")["ready"] is True, "the next passing probe clears it"


@pytest.mark.platforms("posix")
def test_a_probe_of_another_version_is_not_shown(node):
    _probe(node)
    lina_runner_probe.record(node, "faketool", {"state": "failed", "why": "x", "version": "3.0.0", "at": 1})
    row = lina_runners.row(node, lina_runners.pins()["faketool"])
    assert row["probe"] is None and row["ready"] is True


@pytest.mark.platforms("posix")
def test_a_busy_node_is_not_probed(node, monkeypatch):
    monkeypatch.setattr(slots, "held", lambda worker_root: 1)
    assert _probe(node)["results"]["faketool"]["state"] == "busy"
    assert lina_runner_probe.results(node) == {}
