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
              "wc", "date", "sleep", "mkdir", "chmod", "rm", "cp", "mv", "nice", "comm", "join")
AGENT = "agent-a"


def _node(tmp_path: Path, monkeypatch, scan_s: int = 20, extra: str = "", first: str = "done",
          nas_extra: str = "", deny_extra: str = "") -> dict:
    nas, base, hub_bin = tmp_path / "nas", tmp_path / "lina", tmp_path / "hub-bin"
    home, node_bin, volume = nas / "home", nas / "bin", nas / "volume"
    for path in (home / ".bun" / "bin", node_bin, hub_bin, volume / "backup", volume / "share" / "#recycle"):
        path.mkdir(parents=True, exist_ok=True)
    for tool in NODE_TOOLS:
        (node_bin / tool).symlink_to(shutil.which(tool))
    real_find = shutil.which("find")
    (node_bin / "find").write_text(f'#!/bin/sh\ncase "$*" in *-writable*)  # a find of the denied-folder look\n'
                                   f'  {shutil.which("cut")} -d" " -f19 /proc/$$/stat >> "$FAKE_NAS/find.nice" ;; esac\n'
                                   f'{real_find} "$@"; rc=$?\n'  # slow_find / slow_before: a look that runs out of time
                                   f'case "$*" in *-newer*) [ -f "$FAKE_NAS/slow_find" ] && sleep 30 ;;\n'
                                   f'  *"-type f"*) [ -f "$FAKE_NAS/slow_before" ] && sleep 30 ;; esac\nexit $rc\n')
    # the look's own sorts (sort -o) note the memory limit they run under
    (node_bin / "sort").write_text(f'#!/bin/sh\ncase "$*" in *-o*) ulimit -v >> "$FAKE_NAS/sort.ulimit" ;; esac\n'
                                   f'exec {shutil.which("sort")} "$@"\n')
    (node_bin / "curl").write_text('#!/bin/sh\ncat "$FAKE_NAS/healthz" 2>/dev/null || printf 000\n')
    (nas / "healthz").write_text("200")
    fakes.install_fake_omo(nas / "omo", "")
    shutil.move(str(nas / "omo" / "omo"), str(home / ".bun" / "bin" / "omo"))
    (nas / "omo" / "scenario.json").write_text(json.dumps({"first": first}))
    (hub_bin / "ssh").write_text(textwrap.dedent(f"""\
        #!{sys.executable}
        import os, subprocess, sys, time
        nas = {str(nas)!r}
        with open(os.path.join(nas, "ssh.log"), "a") as fh:
            fh.write(sys.argv[-2] + "\\n")
        env = {{"HOME": os.path.join(nas, "home"), "PATH": os.path.join(nas, "bin"), "FAKE_NAS": nas,
               "FAKE_OMO_DIR": os.path.join(nas, "omo"), "LANG": "C.UTF-8"}}
        faults = os.path.join(nas, "launch_faults")  # one line per start: fail (never sent) or lose (answer lost)
        if "runner.sh" in sys.argv[-1] and os.path.exists(faults):
            pending = open(faults).read().split()
            if pending:
                open(faults, "w").write("\\n".join(pending[1:]))
                if pending[0] == "hold":  # a start still on its way while the test acts
                    open(os.path.join(nas, "launch_waiting"), "w").close()
                    while not os.path.exists(os.path.join(nas, "launch_go")):
                        time.sleep(0.05)
                    os.execve("/bin/sh", ["sh", "-c", sys.argv[-1]], env)
                if pending[0] == "lose":
                    subprocess.run(["/bin/sh", "-c", sys.argv[-1]], env=env, capture_output=True)
                sys.exit(255)
        os.execve("/bin/sh", ["sh", "-c", sys.argv[-1]], env)
        """))
    for script in (node_bin / "find", node_bin / "sort", node_bin / "curl", hub_bin / "ssh"):
        script.chmod(0o755)
    monkeypatch.setenv("PATH", f"{hub_bin}{os.pathsep}{os.environ['PATH']}")
    base.mkdir(parents=True)
    (base / "config.yaml").write_text(textwrap.dedent(f"""\
        devices:
          workers:
            nas: {{host: nas-host, kind: file, omo: "~/.bun/bin/omo", deny: ["{volume}/backup", "#recycle"{deny_extra}],
                   volumes: ["{volume}"], scan_s: {scan_s}{nas_extra}}}
            build: {{host: local, cap: 1}}
        worker:
          poll_s: 0.05
          min_free_gb: 0
          access: {{{AGENT}: full}}
        """) + extra, encoding="utf-8")
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


def test_a_file_node_run_gets_the_node_rules_with_its_deny_list(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    task_id = _submit(node)
    prompt = (_ended(node, task_id)["remote"] / "prompt.1").read_text()
    assert "Work only inside the current directory" not in prompt
    assert "Move and change files only inside the paths the brief names" in prompt
    assert f"  - {node['volume']}/backup\n  - #recycle\n" in prompt
    assert "Do not delete anything" in prompt and "## 다음에 알아야 할 것" in prompt


def _abort_watch(node: dict, stubborn: bool, slow_open: bool = False) -> Path:
    """Wrap the node's omo so every abort is logged, and (stubborn) refused while the session keeps running; with
    slow_open, `host session open` waits until the test drops `open_go` beside the node."""
    omo = node["home"] / ".bun" / "bin" / "omo"
    omo.rename(omo.with_name("omo.fake"))
    log = node["nas"] / "aborts.log"
    refuse = 'echo \'{"action":"error","reason":"refused"}\'; exit 1' if stubborn else ":"
    gate = 'if [ "$3" = open ]; then until [ -f "$FAKE_NAS/open_go" ]; do sleep 0.05; done; fi\n' if slow_open else ""
    omo.write_text(f'#!/usr/bin/env bash\n{gate}if [ "$3" = abort ]; then echo abort >> "{log}"; {refuse}; fi\n'
                   f'exec "{omo.with_name("omo.fake")}" "$@"\n')
    omo.chmod(0o755)
    return log


def test_the_hard_limit_aborts_the_session_and_sees_it_stop(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, extra="  hard_timeout_s: 1\n", first="running")
    aborts = _abort_watch(node, stubborn=False)
    task_id = _submit(node)
    remote = _ended(node, task_id)["remote"]
    assert aborts.read_text().count("abort") == 1
    assert (remote / "outcome.1").read_text().strip() == "timeout_stopped"
    assert json.loads((node["nas"] / "omo" / "state.json").read_text())["phase"] == "cancelled"
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "stalled"


def test_a_session_that_does_not_stop_at_the_hard_limit_is_aborted_again_before_the_slot_frees(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, extra="  hard_timeout_s: 1\n", first="running")
    aborts = _abort_watch(node, stubborn=True)
    task_id = _submit(node)
    remote = _ended(node, task_id)["remote"]
    assert (remote / "outcome.1").read_text().strip() == "timeout_not_stopped"
    hub = Hub(node["root"])
    hub.tick()
    job = hub.store.load(task_id)
    assert job["state"] == "stalled" and aborts.read_text().count("abort") == 2
    assert hub.transport(job).call(["probe", "", "omo"]).json()["running"] == 0


def test_an_unreadable_denied_folder_ends_the_run_as_review_not_seen_whole(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    locked = node["volume"] / "backup" / "locked"
    locked.mkdir()
    locked.chmod(0o300)  # the run's user may write there but cannot read it: a change could hide there
    try:
        task_id = _submit(node)
        _ended(node, task_id)
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "review"
        text = _notice(node, task_id)
        assert "the denied-folder check did not finish" in text and "find exited 1" in text
    finally:
        locked.chmod(0o755)


def test_only_changes_owned_by_the_runs_user_count_against_the_run():
    from hermes_cli.worker import file_node_deny as deny
    other_only = "complete=1\nbefore=1\nuid=1026\ncount=3\nfiles:\n0\t/v/backup/a\n1024\t/v/@synologydrive/b\n0\t/v/s/@eaDir/c\n"
    check = deny.parse_scan(other_only, 0, 300)
    assert (check["count"], check["others"], deny.needs_review(check)) == (0, 3, False)
    assert "Changed by other users: 3, not counted" in deny.with_check("", check)
    mixed = other_only.replace("count=3", "count=4") + "1026\t/v/backup/moved.txt\n"
    check = deny.parse_scan(mixed, 0, 300)
    assert (check["files"], check["others"], deny.needs_review(check)) == (["/v/backup/moved.txt"], 3, True)


def _devices_row(node: dict) -> dict:
    from hermes_cli import lina_devices
    lines: list[str] = []
    lina_devices.list_devices(node["root"].parent, as_json=True, out=lines.append)
    return next(row for row in json.loads(lines[-1]) if row["name"] == "nas")


def test_lina_devices_shows_a_file_node_from_its_hello_without_asking_lina_on_it(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    row = _devices_row(node)
    assert (row["ready"], row["why"], row["omo"]) == (True, None, "?")  # the fake omo prints no version line
    assert "lina omo status" not in (node["nas"] / "ssh.log").read_text()
    (node["nas"] / "healthz").write_text("000")
    row = _devices_row(node)
    assert row["ready"] is False and row["why"].startswith("model path through the hub does not answer")


def test_a_file_moved_into_a_denied_folder_is_caught_by_its_ctime(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    old = node["volume"] / "share" / "old-report.txt"
    old.write_text("kept")
    os.utime(old, (946684800, 946684800))  # 2000-01-01: its mtime is long before the run
    task_id = _submit(node)
    _ended(node, task_id)
    old.rename(node["volume"] / "backup" / "old-report.txt")  # the move keeps the mtime, sets a new ctime
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    assert f"{node['volume']}/backup/old-report.txt" in _notice(node, task_id)


def _opens(node: dict) -> int:
    calls = (node["nas"] / "omo" / "calls.jsonl").read_text().splitlines()
    return sum(1 for line in calls if json.loads(line)[:3] == ["host", "session", "open"])


def test_a_start_that_never_reached_the_node_is_started_on_the_next_tick(tmp_path, monkeypatch):
    from hermes_cli.worker import file_node
    monkeypatch.setattr(file_node, "LAUNCH_GAP_S", 0.0)
    node = _node(tmp_path, monkeypatch)
    (node["nas"] / "launch_faults").write_text("fail\n")
    task_id = _submit(node)
    assert not (node["home"] / "lina-file-node" / task_id / "pid.1").exists()
    Hub(node["root"]).tick()
    _ended(node, task_id)
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "done" and _opens(node) == 1


def test_a_start_whose_answer_was_lost_is_adopted_and_never_run_twice(tmp_path, monkeypatch):
    from hermes_cli.worker import file_node
    monkeypatch.setattr(file_node, "LAUNCH_GAP_S", 0.0)
    node = _node(tmp_path, monkeypatch)
    (node["nas"] / "launch_faults").write_text("lose\n")
    task_id = _submit(node)
    _ended(node, task_id)
    hub = Hub(node["root"])
    hub.tick()
    hub.tick()
    assert hub.store.load(task_id)["state"] == "done" and _opens(node) == 1
    events = (node["root"] / "file_nodes" / "nas" / "tasks" / task_id / "events.1.jsonl").read_text()
    assert '"adopted": true' in events


def test_a_start_that_keeps_failing_ends_failed_and_frees_the_slot(tmp_path, monkeypatch):
    from hermes_cli.worker import file_node
    monkeypatch.setattr(file_node, "LAUNCH_GAP_S", 0.0)
    monkeypatch.setattr(file_node, "LAUNCH_TRIES", 2)
    node = _node(tmp_path, monkeypatch)
    (node["nas"] / "launch_faults").write_text("fail\n" * 9)
    task_id = _submit(node)
    hub = Hub(node["root"])
    hub.tick()
    hub.tick()
    job = hub.store.load(task_id)
    assert job["state"] == "failed" and "launch failed after 2 tries" in _notice(node, task_id)
    assert hub.transport(job).call(["probe", "", "omo"]).json()["named_running"] == 0


def test_a_follow_up_waits_for_a_free_slot_before_its_run_starts(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, nas_extra=", named_cap: 1")
    first = _submit(node)
    _ended(node, first)
    hub = Hub(node["root"], out=lambda line: None)
    hub.tick()
    (node["nas"] / "omo" / "scenario.json").write_text(json.dumps({"first": "running"}))
    second = _submit(node)
    session = node["nas"] / "omo" / "state.json"
    wait_until(lambda: session.exists() and json.loads(session.read_text())["phase"] == "running",
               what="the second run's session")
    hub.send(first, "one more file")
    first_remote = node["home"] / "lina-file-node" / first
    hub.tick()
    assert not (first_remote / "prompt.2").exists()  # the second run holds the only slot
    state = json.loads(session.read_text())
    session.write_text(json.dumps({**state, "phase": "done"}))
    wait_until(lambda: (node["home"] / "lina-file-node" / second / "rc.1").exists(), what="the second run's end")
    hub.tick()
    hub.tick()
    wait_until(lambda: (first_remote / "rc.2").exists(), what="the follow-up run's end")
    hub.tick()
    assert (hub.store.load(second)["state"], hub.store.load(first)["state"]) == ("done", "done")


def _two_waiting_behind_a_third(tmp_path, monkeypatch) -> tuple[dict, Hub, list[str], str]:
    """cap 1: two runs ended, a third holds the slot, both ended runs get a follow-up, then the third ends."""
    node = _node(tmp_path, monkeypatch, nas_extra=", named_cap: 1")
    hub = Hub(node["root"], out=lambda line: None)
    done = []
    for _ in range(2):
        done.append(_submit(node))
        _ended(node, done[-1])
        hub.tick()
    (node["nas"] / "omo" / "scenario.json").write_text(json.dumps({"first": "running"}))
    third = _submit(node)
    session = node["nas"] / "omo" / "state.json"
    wait_until(lambda: json.loads(session.read_text())["phase"] == "running", what="the third run's session")
    for task_id in done:
        hub.send(task_id, "one more file")
    hub.tick()
    session.write_text(json.dumps({**json.loads(session.read_text()), "phase": "done"}))
    wait_until(lambda: (node["home"] / "lina-file-node" / third / "rc.1").exists(), what="the third run's end")
    return node, hub, done, third


def _started(node: dict, task_id: str) -> bool:
    return (node["home"] / "lina-file-node" / task_id / "prompt.2").exists()


def test_two_waiting_follow_ups_start_one_after_the_other_once_the_slot_frees(tmp_path, monkeypatch):
    node, hub, (first, second), third = _two_waiting_behind_a_third(tmp_path, monkeypatch)
    hub.tick()
    hub.tick()
    assert hub.store.load(third)["state"] == "done"
    assert (_started(node, first), _started(node, second)) == (True, False)  # the one that waited longest
    wait_until(lambda: (node["home"] / "lina-file-node" / first / "rc.2").exists(), what="the first follow-up's end")
    hub.tick()
    hub.tick()
    wait_until(lambda: (node["home"] / "lina-file-node" / second / "rc.2").exists(), what="the second follow-up's end")
    hub.tick()
    assert [hub.store.load(t)["state"] for t in (first, second)] == ["done", "done"]


def test_two_pending_checks_at_once_start_only_one_waiting_run(tmp_path, monkeypatch):
    import threading
    node, hub, waiting, third = _two_waiting_behind_a_third(tmp_path, monkeypatch)
    hub.transport(hub.store.load(third)).call(["meta", third])  # the third run ends here: its slot is free
    contact = hub.transport(hub.store.load(waiting[0])).raw
    gate = threading.Barrier(4)

    def pending(task_id: str) -> None:
        gate.wait()
        contact._pending(contact._task(task_id), 2)

    threads = [threading.Thread(target=pending, args=(task_id,)) for task_id in waiting * 2]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    assert sum(_started(node, task_id) for task_id in waiting) == 1
    assert contact.call(["probe", "", "omo"]).json()["named_running"] == 1


def _follow_up_waiting_behind_a_running_run(tmp_path, monkeypatch) -> tuple[dict, Hub, str, Path]:
    node = _node(tmp_path, monkeypatch, nas_extra=", named_cap: 1")
    hub = Hub(node["root"], out=lambda line: None)
    first = _submit(node)
    _ended(node, first)
    hub.tick()
    (node["nas"] / "omo" / "scenario.json").write_text(json.dumps({"first": "running"}))
    _submit(node)
    session = node["nas"] / "omo" / "state.json"
    wait_until(lambda: json.loads(session.read_text())["phase"] == "running", what="the second run's session")
    hub.send(first, "one more file")
    return node, hub, first, session


def test_a_waiting_run_does_not_start_while_the_model_path_is_down(tmp_path, monkeypatch):
    node, hub, first, session = _follow_up_waiting_behind_a_running_run(tmp_path, monkeypatch)
    (node["nas"] / "healthz").write_text("000")
    session.write_text(json.dumps({**json.loads(session.read_text()), "phase": "done"}))
    second = next(job["id"] for job in hub.store.all() if job["id"] != first)
    wait_until(lambda: (node["home"] / "lina-file-node" / second / "rc.1").exists(), what="the second run's end")
    hub.tick()
    hub.tick()
    mirror = node["root"] / "file_nodes" / "nas" / "tasks" / first
    assert not _started(node, first) and not (mirror / "launch.2.json").exists()  # waiting, not a failed start
    meta = hub.transport(hub.store.load(first)).call(["meta", first]).json()
    assert meta["state"] == "new" and "model path through the hub does not answer" in meta["live"]["dash"]
    (node["nas"] / "healthz").write_text("200")
    hub.tick()
    assert _started(node, first)


def test_a_folder_moved_into_a_denied_folder_is_caught_with_its_old_files(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    folder = node["volume"] / "share" / "old-photos"
    folder.mkdir()
    for name in ("a.jpg", "b.jpg"):
        (folder / name).write_text(name)
        os.utime(folder / name, (946684800, 946684800))
    os.utime(folder, (946684800, 946684800))
    task_id = _submit(node)
    _ended(node, task_id)
    folder.rename(node["volume"] / "backup" / "old-photos")  # only the folder's ctime changes
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    text = _notice(node, task_id)
    moved = f"{node['volume']}/backup/old-photos"
    assert f"  - {moved}/\n" in text and f"{moved}/a.jpg" in text and f"{moved}/b.jpg" in text


def _aliased(tmp_path, monkeypatch) -> dict:
    """A container app's bind mounts under @appdata: an undenied share (home) and a denied one (backup)."""
    from hermes_cli.worker import file_node_deny
    node = _node(tmp_path, monkeypatch, deny_extra=', "@*"')
    shares = node["volume"] / "@appdata" / "apps" / "all_shares"
    for name in ("home", "backup"):
        (shares / name).mkdir(parents=True)
    mounts = node["nas"] / "mounts"
    mounts.write_text(f"fake {node['volume']} btrfs rw,subvol=/@vol 0 0\n"
                      f"fake {shares}/home btrfs rw,subvol=/@vol/home 0 0\n"
                      f"fake {shares}/backup btrfs rw,subvol=/@vol/backup 0 0\n")
    monkeypatch.setattr(file_node_deny, "MOUNTS", str(mounts))
    return {**node, "shares": shares}


def test_changes_seen_through_a_bind_mount_of_an_undenied_share_are_not_counted(tmp_path, monkeypatch):
    node = _aliased(tmp_path, monkeypatch)
    task_id = _submit(node)
    _ended(node, task_id)
    (node["shares"] / "home" / "state.json").write_text("{}")
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "done"
    assert "Aliases skipped: 1" in _notice(node, task_id)


def test_changes_through_a_bind_mount_of_a_denied_folder_still_count(tmp_path, monkeypatch):
    node = _aliased(tmp_path, monkeypatch)
    task_id = _submit(node)
    _ended(node, task_id)
    (node["shares"] / "backup" / "moved.txt").write_text("x")
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    assert f"{node['shares']}/backup/moved.txt" in _notice(node, task_id)


def test_a_folder_the_run_can_neither_read_nor_write_counts_as_seen(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    locked = node["volume"] / "backup" / "locked"
    locked.mkdir()
    locked.chmod(0)
    try:
        task_id = _submit(node)
        _ended(node, task_id)
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "done"
        assert "Not writable by the run's user, not looked into: 1 folder(s)" in _notice(node, task_id)
    finally:
        locked.chmod(0o755)


def test_status_names_the_denied_folder_check_as_the_reason_for_review(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    task_id = _submit(node)
    _ended(node, task_id)
    (node["volume"] / "backup" / "moved.txt").write_text("x")
    Hub(node["root"]).tick()
    lines: list[str] = []
    Hub(node["root"], out=lines.append).status(task_id)
    text = "\n".join(lines)
    assert "needs review — files changed under a denied folder" in text and "section of report.md" not in text


def test_a_cancel_while_the_session_still_opens_stops_the_session_before_the_run_ends(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, first="running")
    aborts = _abort_watch(node, stubborn=False, slow_open=True)
    task_id = _submit(node)
    remote = node["home"] / "lina-file-node" / task_id
    wait_until(lambda: (remote / "pid.1").exists(), what="the runner's start")
    hub = Hub(node["root"], out=lambda line: None)
    hub.cancel(task_id, "not now")
    assert not (remote / "session.json").exists()  # nothing the hub could abort yet
    (node["nas"] / "open_go").write_text("")
    wait_until(lambda: (remote / "rc.1").exists(), what="the runner's end mark")
    assert (remote / "outcome.1").read_text().strip() == "cancelled_stopped" and aborts.read_text().count("abort") == 1
    assert json.loads((node["nas"] / "omo" / "state.json").read_text())["phase"] == "cancelled"
    hub.tick()
    job = hub.store.load(task_id)
    assert job["state"] == "cancelled" and hub.transport(job).call(["probe", "", "omo"]).json()["named_running"] == 0


def test_a_cancel_whose_abort_fails_says_so(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, first="running")
    _abort_watch(node, stubborn=True)
    task_id = _submit(node)
    remote = node["home"] / "lina-file-node" / task_id
    wait_until(lambda: (remote / "session.json").exists(), what="the run's session")
    with pytest.raises(HubError, match="refused"):
        Hub(node["root"], out=lambda line: None).cancel(task_id, "not now")


def test_two_follow_ups_at_once_both_keep_their_instruction(tmp_path, monkeypatch):
    import threading
    from hermes_cli.worker.file_node import FileNodeTransport
    node = _node(tmp_path, monkeypatch, nas_extra=", named_cap: 1")
    hub = Hub(node["root"], out=lambda line: None)
    first = _submit(node)
    _ended(node, first)
    hub.tick()
    (node["nas"] / "omo" / "scenario.json").write_text(json.dumps({"first": "running"}))
    _submit(node)  # holds the only slot, so neither follow-up starts a run while they race
    session = node["nas"] / "omo" / "state.json"
    wait_until(lambda: json.loads(session.read_text())["phase"] == "running", what="the second run's session")
    contact = hub.transport(hub.store.load(first)).raw
    gate, capacity = threading.Barrier(2), FileNodeTransport._capacity

    def both_read_the_run_first(self, task):
        gate.wait(timeout=10)  # each follow-up has read the run number before either writes
        return capacity(self, task)

    monkeypatch.setattr(FileNodeTransport, "_capacity", both_read_the_run_first)
    replies = []

    def follow_up(text: str) -> None:
        payload = {"prompt": f"ulw {text}", "message": text, "access": "full"}
        replies.append(contact.call(["follow_up", first], json.dumps(payload).encode()).json())

    threads = [threading.Thread(target=follow_up, args=(text,)) for text in ("left instruction", "right instruction")]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join()
    mirror = node["root"] / "file_nodes" / "nas" / "tasks" / first
    prompts = "".join(path.read_text() for path in sorted(mirror.glob("prompt.*")) if path.name != "prompt.1")
    assert "left instruction" in prompts and "right instruction" in prompts, replies


def _unwritable(folder: Path) -> None:
    for path in sorted([folder, *folder.rglob("*")], reverse=True):
        if path.is_dir():
            path.chmod(0o555)


def _writable_again(folder: Path) -> None:
    for path in [folder, *folder.rglob("*")]:
        if path.is_dir():
            path.chmod(0o755)


def test_a_large_tree_the_run_cannot_write_is_not_looked_into(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    big = node["volume"] / "backup" / "big"
    for part in range(30):
        (big / f"part-{part}").mkdir(parents=True)
        for index in range(100):
            (big / f"part-{part}" / f"{index}.dat").write_text("x")
    task_id = _submit(node)
    _ended(node, task_id)
    (big / "part-0" / "new-after-start.dat").write_text("y")  # would count if the look went in
    _unwritable(big)
    try:
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "done"
        assert "Not writable by the run's user, not looked into: 1 folder(s)" in _notice(node, task_id)
    finally:
        _writable_again(big)


def test_a_writable_folder_inside_an_unwritable_one_is_not_seen(tmp_path, monkeypatch):
    """The gap of skipping unwritable folders, kept on purpose: a folder the run may write below one it may not
    (DSM ACLs) is not looked into, and the notice says so."""
    node = _node(tmp_path, monkeypatch)
    closed = node["volume"] / "backup" / "closed"
    (closed / "open").mkdir(parents=True)
    task_id = _submit(node)
    _ended(node, task_id)
    (closed / "open" / "changed.txt").write_text("z")
    closed.chmod(0o555)
    try:
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "done"
        assert "a folder it may write inside one of them (DSM ACLs) is not seen either" in _notice(node, task_id)
    finally:
        closed.chmod(0o755)


def test_a_look_that_runs_out_of_time_still_reports_what_it_skipped(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, scan_s=1)
    closed = node["volume"] / "backup" / "closed"
    closed.mkdir()
    task_id = _submit(node)
    _ended(node, task_id)
    closed.chmod(0o555)
    (node["nas"] / "slow_find").write_text("")
    try:
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "review"
        text = _notice(node, task_id)
        assert "not finished in 1s" in text and "Not writable by the run's user, not looked into: 1 folder(s)" in text
    finally:
        closed.chmod(0o755)


def test_the_look_runs_at_nice_19_and_never_walks_snapshots_or_unwritable_trees(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, deny_extra=', "#snapshot", "@sharesnap"')
    snapshot = node["volume"] / "@sharesnap" / "share" / "daily"
    tagged = node["volume"] / "share" / "#snapshot" / "monday"
    big = node["volume"] / "backup" / "big"
    for folder in (snapshot, tagged, *(big / f"part-{n}" for n in range(20))):
        folder.mkdir(parents=True)
    task_id = _submit(node)
    _ended(node, task_id)
    for folder in (snapshot, tagged, big / "part-3"):  # writable snapshots too: a change there would count if walked
        (folder / "seen-if-walked.txt").write_text("x")
    _unwritable(big)
    try:
        hub = Hub(node["root"])
        hub.tick()
        assert hub.store.load(task_id)["state"] == "done"
        assert "seen-if-walked" not in _notice(node, task_id)
        assert set((node["nas"] / "find.nice").read_text().split()) == {"19"}  # every find of the look
    finally:
        _writable_again(big)


def test_a_cancel_while_the_start_is_on_its_way_cancels_the_run_on_the_node(tmp_path, monkeypatch):
    import threading
    node = _node(tmp_path, monkeypatch, first="running")
    aborts = _abort_watch(node, stubborn=False)
    (node["nas"] / "launch_faults").write_text("hold\n")
    ids: list[str] = []
    submit = threading.Thread(target=lambda: ids.append(_submit(node)))
    submit.start()
    wait_until(lambda: (node["nas"] / "launch_waiting").exists(), what="the start on its way")
    tasks = node["root"] / "file_nodes" / "nas" / "tasks"
    task_id = next(path.name for path in tasks.iterdir())
    cancel = threading.Thread(target=lambda: Hub(node["root"], out=lambda line: None).cancel(task_id, "not now"))
    cancel.start()
    wait_until(lambda: (tasks / task_id / "cancelled").exists(), what="the cancel mark")
    assert not (tasks / task_id / "rc.1").exists()  # the slot is kept while the start is on its way
    (node["nas"] / "launch_go").write_text("")
    submit.join()
    cancel.join()
    remote = node["home"] / "lina-file-node" / task_id
    wait_until(lambda: (remote / "rc.1").exists(), what="the run's end on the node")
    assert (remote / "cancelled").exists() and (remote / "outcome.1").read_text().strip() == "cancelled_stopped"
    assert aborts.read_text().count("abort") == 1
    hub = Hub(node["root"])
    hub.tick()
    job = hub.store.load(task_id)
    assert job["state"] == "cancelled" and hub.transport(job).call(["probe", "", "omo"]).json()["named_running"] == 0


def _kept(node: dict) -> Path:
    kept = node["volume"] / "backup" / "kept.txt"
    kept.write_text("kept")
    return kept


def test_a_file_the_run_moves_out_of_a_denied_folder_ends_review_naming_it(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    kept = _kept(node)
    task_id = _submit(node)
    _ended(node, task_id)
    kept.rename(node["volume"] / "share" / "kept.txt")
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    text = _notice(node, task_id)
    assert "needs review — files removed from a denied folder" in text
    assert f"owned by the run's user: 1:\n  - {kept}\n" in text


def test_a_file_the_run_deletes_in_a_denied_folder_ends_review(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    kept = _kept(node)
    task_id = _submit(node)
    _ended(node, task_id)
    kept.unlink()
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review" and f"  - {kept}\n" in _notice(node, task_id)


def test_a_file_of_another_user_gone_from_a_denied_folder_is_only_counted(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    task_id = _submit(node)
    remote = _ended(node, task_id)["remote"]
    before = remote / "deny_before.1"  # as if a backup service's own file had been there and is gone now
    lines = before.read_text().splitlines() + [f"{node['volume']}/backup/ghost.txt\t4242"]
    before.write_text("".join(f"{line}\n" for line in sorted(set(lines))))
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "done"
    assert "Removed, owned by other users: 1" in _notice(node, task_id)


def test_a_list_before_the_run_that_runs_out_of_time_leaves_the_check_incomplete(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch, scan_s=1)
    (node["nas"] / "slow_before").write_text("")
    task_id = _submit(node)
    _ended(node, task_id)
    (node["nas"] / "slow_before").unlink()
    hub = Hub(node["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    assert "the list taken before the run did not finish (not finished in 1s)" in _notice(node, task_id)


def test_the_looks_run_under_their_memory_limit(tmp_path, monkeypatch):
    node = _node(tmp_path, monkeypatch)
    task_id = _submit(node)
    _ended(node, task_id)
    Hub(node["root"]).tick()
    assert set((node["nas"] / "sort.ulimit").read_text().split()) == {"1048576"}  # before and after alike

    from hermes_cli.worker import file_node_deny
    small = _node(tmp_path / "small", monkeypatch)
    monkeypatch.setattr(file_node_deny, "SCAN_MEMORY_KB", 1024)  # far too little for find: the look cannot run
    task_id = _submit(small)
    _ended(small, task_id)
    hub = Hub(small["root"])
    hub.tick()
    assert hub.store.load(task_id)["state"] == "review"
    assert "out of memory under ulimit -v 1024 KiB" in _notice(small, task_id)


def test_device_roles_and_file_node_keys_are_read_from_config():
    found = {dev.name: dev for dev in settings.devices(raw={
        "a": {"host": "a"}, "nas": {"host": "nas", "kind": "file", "deny": ["/v/backup", "@*"], "volumes": ["/v"]},
        "p": {"host": "p", "role": "person"}})}
    assert (found["a"].role, found["a"].kind) == ("work", "lina")
    assert (found["nas"].role, found["nas"].deny, found["nas"].volumes) == ("files", ("/v/backup", "@*"), ("/v",))
    assert found["p"].role == "person"
