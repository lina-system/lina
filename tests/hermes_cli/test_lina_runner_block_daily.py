"""Adding a tool is one block of settings plus its probe (lina#531): a fake tool that exists only as
``worker.runners.faketool`` in the hub's config is installed, moved to its newest version after a trial, probed and
listed by one daily ``lina runners sync`` -- no code and no ``pm/lina-deps.json`` entry names it.

Its "releases" are Python scripts a fake HTTP source serves; the hub really downloads, checks, installs, trials and
probes them. Nothing reaches the network.
"""
import hashlib
import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_deps, lina_runner_sync, lina_runners
from hermes_cli import lina_runner_sync_state as state
from hermes_cli.lina_runner_sync import Env
from hermes_cli.lina_runner_sync_sources import SourceError

CHANNEL = "https://fake.invalid/faketool/stable"
ARTIFACT = "https://fake.invalid/faketool-{version}-{platform}"


def release(version: str) -> bytes:
    return (f"#!{sys.executable}\nimport sys\nif '--version' in sys.argv:\n    print('faketool {version}')\n"
            "    sys.exit(0)\nprint('OK')\n").encode()


class Source:
    def __init__(self, newest: str):
        self.newest = newest

    def text(self, url: str) -> str:
        if url != CHANNEL:  # the built-in blocks' sources are offline here; the run goes on without them
            raise SourceError(f"{url}: offline in this test")
        return self.newest + "\n"

    def file(self, url: str, dest: Path) -> dict:
        version = url.split("faketool-")[1].rsplit("-", 1)[0]
        Path(dest).write_bytes(release(version))
        return {}


def settings_block() -> dict:
    first = release("1.0.0")
    return {"type": "command", "argv": ["faketool", "-p"],
            "install": {"installer": "binary", "version": "1.0.0", "bin": "faketool", "entry": "faketool",
                        "artifacts": {"linux-x64": {"url": ARTIFACT.format(version="1.0.0", platform="linux"),
                                                    "sha256": hashlib.sha256(first).hexdigest()}},
                        "follow": {"channel": CHANNEL, "artifact": ARTIFACT, "platforms": {"linux-x64": "linux"}}},
            "sign_in": "none", "probe": {"argv": ["-p", "{prompt}"]}}


@pytest.mark.platforms("posix")
def test_a_tool_added_as_one_settings_block_is_installed_probed_and_listed_by_the_daily_run(tmp_path, monkeypatch):
    root = tmp_path / "hub"
    root.mkdir()
    (root / "config.yaml").write_text(json.dumps({"worker": {"runners": {"faketool": settings_block()}}}))
    monkeypatch.setenv("HOME", str(tmp_path))
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    monkeypatch.setattr(lina_deps, "platform_key", lambda: "linux-x64")
    notices: list[str] = []

    def fetch(url: str, dest: Path) -> None:
        assert url == ARTIFACT.format(version="1.0.0", platform="linux")
        Path(dest).write_bytes(release("1.0.0"))

    env = Env(http=Source("1.0.1"), fetch=fetch, nodes=lambda root: [], sign_in=lambda runner: "none",
              notify=lambda root, text: notices.append(text) or "kept")
    assert lina_runners.current_version(root, lina_runners.pins()["faketool"]) is None

    assert lina_runner_sync.sync(root, env=env, out=lambda line: None) == 0

    record = json.loads(state.record_path(root, state.last(root)["started"]).read_text())["runners"]["faketool"]
    assert record["install"] == {"state": "installed", "version": "1.0.0"}
    assert record["hub"]["state"] == "moved" and record["hub"]["to"] == "1.0.1"
    assert record["probes"]["hub"]["state"] == "done" and record["probes"]["hub"]["version"] == "1.0.1"
    listed = {row["name"]: row for row in lina_runners.status(root)}
    assert (listed["faketool"]["installed"], listed["faketool"]["probe"]["state"], listed["faketool"]["ready"]) == \
        ("1.0.1", "done", True)
    offered = {row["name"]: row for row in lina_runners.node_report(root / "worker")}
    assert offered["faketool"]["installed"] == "1.0.1" and offered["faketool"]["probe"]["state"] == "done"
    assert notices == [], "a tool that installed, moved and answered is quiet"
