"""Daily runner care (lina#531): check routes, apply on the hub, probe there before nodes apply,
and probe every node each day. The fake tool runs through the real runner-probe verb."""
import hashlib
import json
import sys
from pathlib import Path

import pytest

from hermes_cli import lina_deps, lina_runner_probe, lina_runner_routes, lina_runner_sync, lina_runners
from hermes_cli import lina_runner_sync_state as state
from hermes_cli.lina_runner_sync import Env, NodeView
from hermes_cli.worker.transport import Reply

CHANNEL = "https://fake.invalid/faketool/stable"
ROUTE: dict = {}


def prepare(root, run):
    return "fake route ready"


def check(root, run):
    ROUTE["checks"].append(root)
    return ROUTE["check"], "route differs" if ROUTE["check"] else "route in step"


def apply(root, run):
    ROUTE["applied"].append(root)
    return 0


def tool(answer: str = "OK") -> bytes:
    return (f"#!{sys.executable}\nimport os, sys\nif '--version' in sys.argv:\n    print('faketool 1.0.0')\n"
            f"    sys.exit(0)\nprint(os.environ.get('FAKETOOL_ANSWER', {answer!r}))\n").encode()


def block(body: bytes) -> dict:
    return {"type": "command", "argv": ["faketool", "-p"],
            "install": {"installer": "binary", "version": "1.0.0", "bin": "faketool", "entry": "faketool",
                        "artifacts": {"linux-x64": {"url": "https://fake.invalid/faketool-1.0.0-linux",
                                                    "sha256": hashlib.sha256(body).hexdigest()}},
                        "follow": {"channel": CHANNEL, "artifact": "https://fake.invalid/faketool-{version}-{platform}",
                                   "platforms": {"linux-x64": "linux"}}},
            "sign_in": "none", "probe": {"argv": ["-p", "{prompt}"]},
            "models": {"file": "~/.faketool/models.json"},
            "route": {"source": "fake-route"}}


def lina_root(path: Path, body: bytes) -> Path:
    path.mkdir(parents=True)
    (path / "config.yaml").write_text(json.dumps({"worker": {"runners": {"faketool": block(body)}}}))
    runner = lina_runners.pins(root=path)["faketool"]
    def fetch(url: str, dest: Path) -> None:
        Path(dest).write_bytes(body)

    lina_runners.install_pin(path, runner, fetch=fetch, out=lambda line: None)
    return path


class Text:
    def text(self, url):
        assert url == CHANNEL
        return "1.0.0\n"

    def file(self, url, dest):
        pytest.fail(f"unexpected download {url}")


@pytest.fixture
def world(tmp_path, monkeypatch):
    monkeypatch.setenv("HOME", str(tmp_path / "acct"))
    monkeypatch.delenv("FAKETOOL_ANSWER", raising=False)
    monkeypatch.setattr(lina_deps, "platform_key", lambda: "linux-x64")
    monkeypatch.setitem(lina_runner_routes.SOURCES, "fake-route", __name__)
    ROUTE.clear()
    ROUTE.update(check=10, checks=[], applied=[])
    hub = lina_root(tmp_path / "hub", tool())
    node = lina_root(tmp_path / "node", tool())
    monkeypatch.setattr(lina_deps, "lina_root", lambda: hub)
    notices: list[str] = []

    def call(verb, data, timeout):
        assert verb == [lina_runner_probe.VERB]
        return Reply(0, json.dumps(lina_runner_probe.apply(node / "worker", json.loads(data))).encode())

    row = {"name": "faketool", "pin": "faketool", "pinned": "1.0.0", "installed": "1.0.0", "sign_in": "none"}
    view = NodeView("build-a", True, platform="linux-x64", verbs=("hello", "runner-sync", lina_runner_probe.VERB),
                    rows={"faketool": row}, call=call)
    env = Env(http=Text(), nodes=lambda root: [view], sign_in=lambda runner: "none",

              notify=lambda root, text: notices.append(text) or "kept")
    return {"hub": hub, "node": node, "env": env, "notices": notices, "row": row}


def sync(world) -> dict:
    assert lina_runner_sync.sync(world["hub"], only=["faketool"], env=world["env"], out=lambda line: None) in (0, 1)
    day = sorted(state.sync_dir(world["hub"]).glob("[0-9]" * 8 + ".json"))[-1]
    return json.loads(day.read_text())["runners"]["faketool"]


@pytest.mark.platforms("posix")
def test_a_differing_route_is_applied_on_the_hub_probed_there_then_applied_and_probed_on_the_nodes(world):
    rec = sync(world)
    assert rec["route"] == {"state": "applied"}
    assert ROUTE["applied"] == [world["hub"], world["node"]], "the hub applies first, the node after its probe passed"
    assert rec["probes"]["hub"]["state"] == "done" and rec["probes"]["build-a"]["state"] == "done"
    assert rec["probes"]["build-a"]["route"] == {"state": "applied"}
    assert lina_runner_probe.results(world["node"])["faketool"]["state"] == "done"


@pytest.mark.platforms("posix")
def test_routes_in_step_are_not_applied_but_probes_run_each_day(world):
    sync(world)
    ROUTE.update(check=0, applied=[])
    rec = sync(world)
    assert rec["route"] == {"state": "in step"} and ROUTE["applied"] == []
    assert rec["probes"]["build-a"]["state"] == "done" and "route" not in rec["probes"]["build-a"]



@pytest.mark.platforms("posix")
def test_a_route_whose_hub_probe_fails_holds_the_runner_and_reaches_no_node(world, monkeypatch):
    monkeypatch.setenv("FAKETOOL_ANSWER", "Authentication required")
    rec = sync(world)
    assert rec["route"]["state"] == "failed" and "probe after apply" in rec["route"]["reason"]
    assert ROUTE["applied"] == [world["hub"]] and "build-a" not in rec.get("probes", {})
    hold = state.held(world["hub"], "faketool", "hub")
    assert hold and hold["kind"] == "route"


@pytest.mark.platforms("posix")
def test_a_route_check_that_fails_holds_the_runner(world):
    ROUTE["check"] = 2
    rec = sync(world)
    assert rec["route"]["state"] == "failed" and ROUTE["applied"] == []
    hold = state.held(world["hub"], "faketool", "hub")
    assert hold and hold["kind"] == "route"


@pytest.mark.platforms("posix")
def test_a_day_where_everything_went_well_sends_no_notice(world):
    rec = sync(world)
    assert rec["route"] == {"state": "applied"}
    assert world["notices"] == [], "quiet when fine"


@pytest.mark.platforms("posix")
def test_a_failed_probe_on_a_node_is_one_line_to_the_owner(world):
    entry = lina_runners.vendor_dir(world["node"], "faketool") / "1.0.0" / "faketool"
    entry.write_bytes(tool("Authentication required"))
    ROUTE["check"] = 0
    rec = sync(world)
    assert rec["probes"]["hub"]["state"] == "done" and rec["probes"]["build-a"]["state"] == "failed"
    assert world["notices"] == ["runner sync " + rec_day(world) + ":\n"
                                "- faketool on build-a: probe failed: did not answer OK: Authentication required\n"]


@pytest.mark.platforms("posix")
def test_a_sign_in_lost_since_the_last_run_is_one_line_to_the_owner(world):
    ROUTE["check"] = 0
    world["row"]["sign_in"] = "signed_in"
    sync(world)
    assert world["notices"] == []
    world["row"]["sign_in"] = "signed_out"
    sync(world)
    assert len(world["notices"]) == 1 and world["notices"][0].splitlines()[1:] == ["- faketool on build-a: sign-in lost"]


def rec_day(world) -> str:
    return sorted(state.sync_dir(world["hub"]).glob("[0-9]" * 8 + ".json"))[-1].stem
