"""A file node (lina#229): the hub drives a device with no LINA, git or recent Python over ssh.

The fake ssh runs the remote command the way the node does: under ``/bin/sh`` (dash here, not bash) with a PATH that
holds only the node's tools: bash, find, tar, sha256sum, setsid, nohup, coreutils and a fake ``curl`` for the model
path. There is no ``timeout``, ``pgrep``, ``getconf``, git or python3 on it. HOME is a temporary folder that plays the
node's volume; omo is the fake omo of ``_worker_fakes`` at ``~/.bun/bin/omo``.
"""

import json
import os
import shutil
import sys
import textwrap
from pathlib import Path

import pytest

from hermes_cli.worker import bidding, settings
from hermes_cli.worker.hub import Hub, HubError
from hermes_cli.worker.settings import Device
from hermes_cli.worker.transport import for_device
from tests.hermes_cli import _worker_fakes as fakes
from tests.waiting import wait_until

NODE_TOOLS = ("bash", "tar", "sha256sum", "setsid", "nohup", "cat", "sed", "cut", "head", "tail", "grep", "df", "tr",
              "wc", "date", "sleep", "mkdir", "chmod", "rm", "cp", "mv")
AGENT = "agent-a"


def _node(tmp_path: Path, monkeypatch, scan_s: int = 20) -> dict:
    nas, base, hub_bin = tmp_path / "nas", tmp_path / "lina", tmp_path / "hub-bin"
    home, node_bin, volume = nas / "home", nas / "bin", nas / "volume"
    for path in (home / ".bun" / "bin", node_bin, hub_bin, volume / "backup", volume / "share" / "#recycle"):
        path.mkdir(parents=True, exist_ok=True)
    for tool in NODE_TOOLS:
        (node_bin / tool).symlink_to(shutil.which(tool))
    real_find = shutil.which("find")
    (node_bin / "find").write_text(f'#!/bin/sh\ncase "$*" in *-newer*) [ -f "$FAKE_NAS/slow_find" ] && sleep 30 ;; esac\n'
                                   f'exec {real_find} "$@"\n')
    (node_bin / "curl").write_text('#!/bin/sh\ncat "$FAKE_NAS/healthz" 2>/dev/null || printf 000\n')
    (nas / "healthz").write_text("200")
    fakes.install_fake_omo(nas / "omo", "")
    shutil.move(str(nas / "omo" / "omo"), str(home / ".bun" / "bin" / "omo"))
    (nas / "omo" / "scenario.json").write_text(json.dumps({"first": "done"}))
    (hub_bin / "ssh").write_text(textwrap.dedent(f"""\
        #!{sys.executable}
        import os, sys
        nas = {str(nas)!r}
        with open(os.path.join(nas, "ssh.log"), "a") as fh:
            fh.write(sys.argv[-2] + "\\n")
        env = {{"HOME": os.path.join(nas, "home"), "PATH": os.path.join(nas, "bin"), "FAKE_NAS": nas,
               "FAKE_OMO_DIR": os.path.join(nas, "omo"), "LANG": "C.UTF-8"}}
        os.execve("/bin/sh", ["sh", "-c", sys.argv[-1]], env)
        """))
    for script in (node_bin / "find", node_bin / "curl", hub_bin / "ssh"):
        script.chmod(0o755)
    monkeypatch.setenv("PATH", f"{hub_bin}{os.pathsep}{os.environ['PATH']}")
    base.mkdir(parents=True)
    (base / "config.yaml").write_text(textwrap.dedent(f"""\
        devices:
          workers:
            nas: {{host: nas-host, kind: file, omo: "~/.bun/bin/omo", deny: ["{volume}/backup", "#recycle"],
                   volumes: ["{volume}"], scan_s: {scan_s}}}
            build: {{host: local, cap: 1}}
        worker:
          poll_s: 0.05
          min_free_gb: 0
          access: {{{AGENT}: full}}
        """), encoding="utf-8")
    return {"nas": nas, "home": home, "volume": volume, "root": base / "worker", "tmp": tmp_path}


def _submit(node: dict, brief: str = "tidy the scratch share", **extra) -> str:
    lines: list[str] = []
    Hub(node["root"], out=lines.append).submit({"briefs": [fakes.brief(node["tmp"], brief)], "mode": "ulw",
                                                "bot": AGENT, "topic": "files", "wait": None, "host": "nas",
                                                "access": "full", **extra})
    return lines[0].split()[0]


def _ended(node: dict, task_id: str) -> dict:
    remote = node["home"] / "lina-file-node" / task_id
    wait_until(lambda: (remote / "rc.1").exists(), what="the runner's end mark on the node")
    return {"remote": remote}


def _notice(node: dict, task_id: str) -> str:
    notices = sorted((node["root"] / "hub" / "notices").glob(f"{task_id}-*.md"))
    return notices[-1].read_text(encoding="utf-8")


def test_a_run_on_a_file_node_ends_done_with_its_result_on_the_hub(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    (tmp_path / "list.txt").write_text("a\nb\n")
    task_id = _submit(node, inputs=[str(tmp_path / "list.txt")])
    remote = _ended(node, task_id)["remote"]
    assert (remote / "work" / "input" / "list.txt").read_text() == "a\nb\n"

    hub = Hub(node["root"])
    hub.tick()
    job = hub.store.load(task_id)
    assert (job["host"], job["state"], job["delivery"]) == ("nas", "done", "ok")
    out = hub.store.out_dir(job)
    assert (out / "out" / "ok.txt").read_text() == "ok\n" and "fake final answer" in (out / "final.md").read_text()
    manifest = json.loads((out / "manifest.json").read_text())
    assert manifest["inputs_unchanged"] is True and manifest["deny_check"]["count"] == 0
    assert "Denied-folder check (a check, not a sandbox" in _notice(node, task_id)
    assert hub.transport(job).call(["probe", "", "omo"]).json()["running"] == 0  # the slot is free again


def test_a_change_under_a_denied_folder_ends_the_run_as_review_naming_the_files(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    task_id = _submit(node)
    _ended(node, task_id)
    (node["volume"] / "backup" / "moved.txt").write_text("x")
    (node["volume"] / "share" / "#recycle" / "old.txt").write_text("y")

    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    text = _notice(node, task_id)
    assert "needs review — files changed under a denied folder" in text
    assert f"{node['volume']}/backup/moved.txt" in text and f"{node['volume']}/share/#recycle/old.txt" in text


def test_a_denied_folder_look_that_runs_out_of_time_ends_as_review(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, scan_s=1)
    task_id = _submit(node)
    _ended(node, task_id)
    (node["nas"] / "slow_find").write_text("")

    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    text = _notice(node, task_id)
    assert "the denied-folder check did not finish" in text and "not finished in 1s" in text


def test_a_model_path_that_does_not_answer_refuses_the_run_and_says_why(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    (node["nas"] / "healthz").write_text("000")
    hub = Hub(node["root"], out=(lines := []).append)
    with pytest.raises(HubError, match="model path through the hub does not answer"):
        _submit(node)
    assert not (node["home"] / "lina-file-node").exists()

    dev = hub.device("nas")
    contact = for_device(dev, node["root"])
    contact.refresh()
    assert (contact.descriptor["model_path"], contact.descriptor["ready"]) == ("down", False)
    hub.hosts()
    assert "model path through the hub does not answer" in next(line for line in lines if line.startswith("nas"))
    from hermes_cli import lina_devices
    row = lina_devices.readiness(dev, node["root"])
    assert row["ready"] is False and "model path through the hub does not answer" in row["why"]


def test_briefs_and_inputs_naming_a_denied_folder_are_refused_at_submit(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    with pytest.raises(HubError, match="the brief names a denied folder"):
        _submit(node, f"move {node['volume']}/backup/2024 into the share")
    with pytest.raises(HubError, match="the brief names a denied folder"):
        _submit(node, "empty share/#recycle/ first")
    trash = tmp_path / "upload" / "#recycle"
    trash.mkdir(parents=True)
    (trash / "x.txt").write_text("x")
    with pytest.raises(HubError, match="an input is under a denied folder"):
        _submit(node, inputs=[str(tmp_path / "upload")])
    assert not (node["nas"] / "ssh.log").exists()  # refused before the node was asked anything


def test_a_file_node_takes_work_only_by_name_and_never_with_a_repository(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    with pytest.raises(HubError, match="a file node takes no repository work"):
        _submit(node, repo="owner/name")

    ready = {"ready": True, "serve": True, "running": 0, "cores": 4, "load": 0.0, "free_gb": 50, "repo": True}
    files, work = Device("nas", "nas", cap=4, role="files", kind="file"), Device("build", "build", cap=1)
    winner, rows = bidding.choose([files, work], [ready, {**ready, "running": 1}], repo=None)
    assert winner is None and rows[0]["why"] == "only when named with --host"
    assert bidding.choose([files], [ready], repo=None, manual=True)[0] is files
    assert bidding.bid([files], node["root"], repo=None, runner="omo") == (None, [])


def test_a_person_device_never_takes_work_even_when_named(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    phone = Device("phone", "phone", role="person")
    ready = {"ready": True, "serve": True, "running": 0, "cores": 4, "load": 0.0, "free_gb": 50, "repo": True}
    winner, rows = bidding.choose([phone], [ready], repo=None, manual=True)
    assert winner is None and rows[0]["why"] == "role person: takes no work"
    config = node["root"].parent / "config.yaml"
    config.write_text(config.read_text().replace("    build:", "    phone: {host: phone, role: person}\n    build:"))
    with pytest.raises(HubError, match="role person: takes no work"):
        lines: list[str] = []
        Hub(node["root"], out=lines.append).submit({"briefs": [fakes.brief(tmp_path, "x")], "mode": "ulw", "bot": AGENT,
                                                    "topic": "t", "wait": None, "host": "phone"})


def test_device_roles_and_file_node_keys_are_read_from_config():
    found = {dev.name: dev for dev in settings.devices(raw={
        "a": {"host": "a"}, "nas": {"host": "nas", "kind": "file", "deny": ["/v/backup", "@*"], "volumes": ["/v"]},
        "p": {"host": "p", "role": "person"}})}
    assert (found["a"].role, found["a"].kind) == ("work", "lina")
    assert (found["nas"].role, found["nas"].deny, found["nas"].volumes) == ("files", ("/v/backup", "@*"), ("/v",))
    assert found["p"].role == "person"
