"""One runner block per worker tool (lina#531): ``worker.runners.<name>`` in the LINA root's config can state how a tool
is installed and pinned, how its sign-in is read, its model list, its route and its probe, and the same
``lina_runners`` code installs and reports it; omo is such a block too, not a separate path.

The fake tool is a Python script served by a fake fetch; nothing reaches the network.
"""
import hashlib
import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_ocx, lina_runners, lina_work_settings
from hermes_cli.lina_runner_routes import source as route_source

TOOL = f"#!{sys.executable}\nprint('faketool 3.1.0')\n".encode()
URL = "https://fake.invalid/faketool"


def block(**extra) -> dict:
    return {"type": "command", "argv": ["faketool", "-"],
            "install": {"installer": "binary", "version": "3.1.0", "bin": "faketool", "entry": "faketool",
                        "artifacts": {lina_ocx.platform_key(): {"url": URL, "sha256": hashlib.sha256(TOOL).hexdigest()}}},
            "sign_in": {"env": "FAKETOOL_API_KEY"}, "probe": {"argv": ["-p", "{prompt}"]},
            "models": {"file": "~/.faketool/models.json"}, **extra}


@pytest.fixture
def root(tmp_path, monkeypatch):
    account = tmp_path / "acct"
    root = account / ".lina"
    root.mkdir(parents=True)
    monkeypatch.setenv("HOME", str(account))
    monkeypatch.delenv("FAKETOOL_API_KEY", raising=False)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)
    (root / "config.yaml").write_text(json.dumps({"worker": {"runners": {"faketool": block()}}}))
    return root


def fetch(url, dest):
    assert url == URL
    Path(dest).write_bytes(TOOL)


@pytest.mark.platforms("posix")
def test_a_tool_described_only_by_its_settings_block_is_installed_and_reported_like_any_other(root, monkeypatch):
    runner = lina_runners.pins()["faketool"]
    assert (runner.version, runner.probe, runner.models["file"]) == ("3.1.0", {"argv": ["-p", "{prompt}"]},
                                                                      "~/.faketool/models.json")
    assert lina_runners.install("faketool", root, fetch=fetch, out=lambda line: None) == 0
    assert lina_runners.current_version(root, runner) == "3.1.0"

    row = lina_runners.row(root, runner)
    assert (row["installed"], row["sign_in"], row["sign_in_why"], row["why"]) == \
        ("3.1.0", "signed_out", "FAKETOOL_API_KEY is not set", "sign in")
    monkeypatch.setenv("FAKETOOL_API_KEY", "set-for-the-test")
    row = lina_runners.row(root, runner)
    assert (row["sign_in"], row["ready"]) == ("api_key", True)

    offered = {name: pin for name, _, pin in lina_runners.offered({"runners": {"faketool": block()}})}
    assert offered["faketool"] == runner


@pytest.mark.platforms("posix")
def test_a_file_sign_in_is_read_by_existence_only(root):
    (root / "config.yaml").write_text(json.dumps(
        {"worker": {"runners": {"faketool": block(sign_in={"file": "~/.faketool/auth.json"})}}}))
    runner = lina_runners.pins()["faketool"]
    assert lina_runners.read_sign_in(root, runner) == ("signed_out", "no ~/.faketool/auth.json")
    auth = root.parent / ".faketool" / "auth.json"
    auth.parent.mkdir()
    auth.write_text("not read")
    assert lina_runners.read_sign_in(root, runner) == ("signed_in", None)


def test_omo_is_a_block_of_the_same_table_with_its_route_source(root):
    omo = lina_runners.pins()["omo"]
    assert omo.installer == "npm" and lina_runners.read_sign_in(root, omo) == ("none", None)
    assert route_source(omo).__name__ == "hermes_cli.lina_omo_routing"
    assert route_source(lina_runners.pins()["grok"]) is None
    assert lina_runners.vendor_dir(root, omo) == root / "vendor" / "omo"
    offered = {name: pin for name, _, pin in lina_runners.offered({})}
    assert offered["omo"] == omo, "omo is pinned by its block, not left out"


@pytest.mark.parametrize(("entry", "path"), [
    (block(install={"installer": "pip", "version": "1"}), "worker.runners.faketool.install.installer"),
    (block(install={"installer": "npm"}), "worker.runners.faketool.install.version"),
    (block(sign_in="browser"), "worker.runners.faketool.sign_in"),
    (block(sign_in={"file": "a", "env": "B"}), "worker.runners.faketool.sign_in"),
    (block(probe=["-p"]), "worker.runners.faketool.probe"),
])
def test_the_work_settings_refuse_a_malformed_block(entry, path):
    errors = lina_work_settings.validate({"worker": {"runners": {"faketool": entry}}})
    assert path in {error["path"] for error in errors}, json.dumps(errors)


def test_the_work_settings_take_a_whole_block():
    assert lina_work_settings.validate({"worker": {"runners": {"faketool": block()}}}) == []
