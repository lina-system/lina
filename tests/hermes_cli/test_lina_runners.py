"""LINA's pinned runners (lina#222): each runner is installed under the LINA home by the installer that owns its
binary, checked before it is kept, upgraded with a trial and a way back, and reported per node with its sign-in and
one reason when it is not ready.

The fake Node is a Python program that does what npm would for ``npm-cli.js`` (the lock, ``ci`` writing the package
and its ``.bin`` entry). Binary and tarball runners come from a fake fetch. Nothing reaches the network, and the
person's own runner on PATH is a script whose bytes and mtime the tests compare.
"""
import hashlib
import io
import json
import os
import subprocess
import sys
import tarfile
import textwrap
from pathlib import Path

import pytest

from hermes_cli import lina_deps, lina_runners, lina_services
from hermes_cli.lina_runners import RunnerPin

INTEGRITY = "sha512-" + "B" * 86 + "=="

_FAKE_NODE = '''#!{python}
import json, os, sys
script, args = sys.argv[1], sys.argv[2:]
if not script.endswith("npm-cli.js"):
    sys.exit(3)
open(os.path.join(os.environ["HOME"], "npm-was-here"), "w").close()
deps = json.load(open("package.json")).get("dependencies", {{}})
if args[0] == "install" and "--package-lock-only" in args:
    lock = {{"packages": {{"node_modules/" + name: {{"version": version, "integrity": {integrity!r}}}
                           for name, version in deps.items()}}}}
    json.dump(lock, open("package-lock.json", "w"))
elif args[0] == "ci":
    for name, version in deps.items():
        os.makedirs(os.path.join("node_modules", name), exist_ok=True)
        json.dump({{"name": name, "version": version}}, open(os.path.join("node_modules", name, "package.json"), "w"))
        os.makedirs(os.path.join("node_modules", ".bin"), exist_ok=True)
        entry = os.path.join("node_modules", ".bin", name.split("/")[-1])
        with open(entry, "w") as fh:
            fh.write({cli!r}.format(python=sys.executable, version=version))
        os.chmod(entry, 0o755)
'''
_FAKE_CLI = '''#!{python}
import os
print("cli " + os.environ.get("FAKE_REPORT", "{version}") + " autoupdater=" + os.environ.get("DISABLE_AUTOUPDATER", "-"))
'''
_FAKE_BINARY = "#!{python}\nprint('grok 1.0.70 (fake) [stable]')\n"


def _tar(files: dict[str, str], mode: int = 0o755) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as archive:
        for path, text in files.items():
            data = text.encode()
            info = tarfile.TarInfo(path)
            info.size, info.mode = len(data), mode
            archive.addfile(info, io.BytesIO(data))
    return buf.getvalue()


def _npm(name: str, package: str, version: str, **extra) -> RunnerPin:
    return RunnerPin(name=name, installer="npm", package=package, version=version, integrity=INTEGRITY,
                     bin=package.split("/")[-1], **extra)


@pytest.fixture
def world(tmp_path, monkeypatch):
    """An account home with a person-installed ``codex`` on PATH and a LINA root; every download is fake."""
    account = tmp_path / "acct"
    root = account / ".lina"
    root.mkdir(parents=True)
    person_bin = account / ".local" / "bin"
    person_bin.mkdir(parents=True)
    person = person_bin / "codex"
    person.write_text(f"#!{sys.executable}\nprint('codex-cli 0.9.0')\n")
    person.chmod(0o755)
    monkeypatch.setenv("HOME", str(account))
    monkeypatch.setenv("PATH", os.pathsep.join([str(person_bin), "/usr/bin", "/bin"]))
    for key in ("CURSOR_API_KEY", "FAKE_REPORT", "DISABLE_AUTOUPDATER"):
        monkeypatch.delenv(key, raising=False)

    real = lina_deps.pins()
    node = _tar({f"node-v{real.node_version}/bin/node": _FAKE_NODE.format(
                    python=sys.executable, integrity=INTEGRITY, cli=_FAKE_CLI),
                 f"node-v{real.node_version}/lib/node_modules/npm/bin/npm-cli.js": "// npm\n"})
    grok = _FAKE_BINARY.format(python=sys.executable).encode()
    cursor = _tar({"dist-package/cursor-agent": f"#!{sys.executable}\nprint('2026.10.22-27dcf5f')\n"})
    blobs = {"https://nodejs.invalid": node, "https://grok.invalid/grok": grok,
             "https://cursor.invalid/agent.tar.gz": cursor}
    monkeypatch.setattr(lina_deps, "pins", lambda path=None: lina_deps.Pins(
        real.node_version, "https://nodejs.invalid/dist",
        {key: hashlib.sha256(node).hexdigest() for key in real.node_sha256}))
    monkeypatch.setattr(lina_deps, "lina_root", lambda: root)
    monkeypatch.setattr(lina_deps, "LOCK_DIR", tmp_path / "npm-locks")
    key = lina_deps.platform_key()
    table = {
        "codex": _npm("codex", "@openai/codex", "1.0.0", sign_in="magpie"),
        "claude": _npm("claude", "@anthropic-ai/claude", "2.0.0", sign_in="magpie", env={"DISABLE_AUTOUPDATER": "1"}),
        "grok": RunnerPin(name="grok", installer="binary", version="1.0.70", bin="grok", entry="grok", sign_in="grok",
                          artifacts={key: {"url": "https://grok.invalid/grok",
                                           "sha256": hashlib.sha256(grok).hexdigest()}}),
        "cursor-agent": RunnerPin(name="cursor-agent", installer="tarball", version="2026.10.22-27dcf5f",
                                  bin="cursor-agent", entry="cursor-agent", strip=1, sign_in="cursor",
                                  artifacts={key: {"url": "https://cursor.invalid/agent.tar.gz",
                                                   "sha512": hashlib.sha512(cursor).hexdigest()}}),
    }
    monkeypatch.setattr(lina_runners, "pins", lambda path=None: dict(table))
    from hermes_cli.worker import device
    proxy = {"url": "http://127.0.0.1:3425/v1/models", "health": 200, "models": 2}
    monkeypatch.setattr(device, "proxy_check", lambda worker_root: dict(proxy))

    def fetch(url, dest):
        Path(dest).write_bytes(next(blob for prefix, blob in blobs.items() if url.startswith(prefix)))

    return {"root": root, "account": account, "person": person, "fetch": fetch, "table": table, "blobs": blobs,
            "proxy": proxy}


def _install(world, name=None) -> int:
    return lina_runners.install(name, world["root"], fetch=world["fetch"], out=lambda line: None)


def _shim(world, name: str) -> str:
    return subprocess.run([str(lina_runners.shim_path(world["root"], name))], capture_output=True, text=True,
                          check=True).stdout.strip()


def _snapshot(path: Path) -> dict:
    return {each: (each.stat().st_mtime_ns, each.read_bytes()) for each in path.rglob("*") if each.is_file()}


@pytest.mark.platforms("posix")
def test_install_puts_every_runner_under_the_lina_home_and_leaves_the_persons_alone(world):
    outside = world["account"] / ".local"
    before = _snapshot(outside)

    assert _install(world) == 0

    root = world["root"]
    for name, version in (("codex", "1.0.0"), ("claude", "2.0.0"), ("grok", "1.0.70"),
                          ("cursor-agent", "2026.10.22-27dcf5f")):
        runner = world["table"][name]
        assert lina_runners.current_version(root, runner) == version
        assert lina_runners.entry_path(root, runner).resolve().is_relative_to(root.resolve())
    assert _shim(world, "codex") == "cli 1.0.0 autoupdater=-"
    assert _shim(world, "claude") == "cli 2.0.0 autoupdater=1", "Claude's documented auto-update switch is off"
    assert _shim(world, "grok") == "grok 1.0.70 (fake) [stable]"
    assert _shim(world, "cursor-agent") == "2026.10.22-27dcf5f"
    assert _snapshot(outside) == before, "the person's runner and bin dir are not touched"
    assert not (world["account"] / "npm-was-here").exists(), "npm runs with LINA's HOME, not the account's"


@pytest.mark.platforms("posix")
def test_an_artifact_whose_hash_is_not_the_pin_is_not_kept(world):
    world["blobs"]["https://grok.invalid/grok"] = b"#!/bin/sh\necho tampered\n"

    assert _install(world, "grok") == 1

    grok = world["table"]["grok"]
    assert lina_runners.current_version(world["root"], grok) is None
    assert lina_runners.installed_versions(world["root"], grok) == []
    assert not lina_runners.shim_path(world["root"], "grok").exists()
    with pytest.raises(lina_runners.RunnersError, match="sha256 mismatch"):
        lina_runners.install_artifact(world["root"], grok, world["fetch"])


@pytest.mark.platforms("posix")
def test_upgrade_trials_the_new_version_then_switches_and_rolls_back(world, monkeypatch):
    assert _install(world, "codex") == 0
    root, lines = world["root"], []
    world["table"]["codex"] = _npm("codex", "@openai/codex", "1.1.0", sign_in="magpie")

    monkeypatch.setenv("FAKE_REPORT", "0.0.1")
    assert lina_runners.upgrade("codex", root, fetch=world["fetch"], out=lines.append) == 1
    assert "trial failed" in lines[-1] and lina_runners.current_version(root, world["table"]["codex"]) == "1.0.0"
    assert _shim(world, "codex").startswith("cli 0.0.1"), "the shim still runs 1.0.0 (FAKE_REPORT speaks for it)"

    monkeypatch.delenv("FAKE_REPORT")
    assert lina_runners.upgrade("codex", root, fetch=world["fetch"], out=lines.append) == 0
    assert lina_runners.current_version(root, world["table"]["codex"]) == "1.1.0"
    assert _shim(world, "codex") == "cli 1.1.0 autoupdater=-"

    assert lina_runners.rollback("codex", root, out=lines.append) == 0
    assert lina_runners.current_version(root, world["table"]["codex"]) == "1.0.0"
    assert lina_runners.rollback("codex", root, out=lines.append) == 1, "one rollback per recorded upgrade"


@pytest.mark.platforms("posix")
def test_upgrade_switches_back_when_the_switched_shim_does_not_answer(world):
    assert _install(world, "codex") == 0
    root, lines = world["root"], []
    world["table"]["codex"] = _npm("codex", "@openai/codex", "1.1.0", sign_in="magpie")
    shim = str(lina_runners.shim_path(root, "codex"))

    def run(argv, **kwargs):
        if argv[0] == shim:
            return subprocess.CompletedProcess(argv, 1, "", "broken")
        kwargs.pop("check", None)
        return subprocess.run(argv, check=False, **kwargs)

    assert lina_runners.upgrade("codex", root, fetch=world["fetch"], run=run, out=lines.append) == 1
    assert "switched back to 1.0.0" in lines[-1]
    assert lina_runners.current_version(root, world["table"]["codex"]) == "1.0.0"
    assert not (lina_runners.vendor_dir(root, "codex") / "previous").exists()


@pytest.mark.platforms("posix")
def test_status_reports_versions_sign_in_and_one_reason_per_runner(world, monkeypatch):
    grok_auth = world["account"] / ".grok" / "auth.json"
    grok_auth.parent.mkdir()
    grok_auth.write_text("secret-token")
    grok_auth.chmod(0)  # the check may only see that it exists
    before = _snapshot(world["account"] / ".local")

    rows = {row["name"]: row for row in lina_runners.status(world["root"])}

    assert rows["codex"]["installed"] is None and rows["codex"]["why"] == "prepare"
    assert rows["codex"]["person_installed"] == {"path": str(world["person"]), "version": "0.9.0"}
    assert rows["grok"]["sign_in"] == "signed_in"
    assert rows["cursor-agent"]["sign_in"] == "signed_out"
    assert "secret-token" not in json.dumps(rows)
    assert _snapshot(world["account"] / ".local") == before

    assert _install(world) == 0
    world["table"]["codex"] = _npm("codex", "@openai/codex", "1.1.0", sign_in="magpie")
    monkeypatch.setenv("CURSOR_API_KEY", "k")
    world["proxy"]["health"] = None
    rows = {row["name"]: row for row in lina_runners.status(world["root"])}
    assert (rows["codex"]["installed"], rows["codex"]["why"]) == ("1.0.0", "update")
    assert (rows["claude"]["sign_in"], rows["claude"]["why"]) == ("unknown", "prepare")
    assert "HTTP none" in rows["claude"]["sign_in_why"]
    assert (rows["cursor-agent"]["sign_in"], rows["cursor-agent"]["ready"]) == ("api_key", True)
    assert rows["grok"]["ready"] is True and rows["grok"]["why"] is None


@pytest.mark.platforms("posix")
def test_node_report_follows_the_runners_the_node_offers(world):
    (world["root"] / "config.yaml").write_text(textwrap.dedent("""\
        worker:
          runners:
            codex: {type: command, argv: [codex, exec, "-"]}
            sonnet: {type: command, argv: [/opt/x/claude, -p]}
            mine: {type: command, argv: [mine]}
        """), encoding="utf-8")
    assert _install(world, "claude") == 0

    rows = lina_runners.node_report(world["root"] / "worker")

    assert [(row["name"], row["type"]) for row in rows] == [
        ("omo", "omo"), ("codex", "command"), ("sonnet", "command"), ("mine", "command")]
    assert rows[0]["pin"] is None, "omo has its own pin and status"
    assert rows[1]["pin"] == "codex" and rows[1]["why"] == "prepare"
    assert rows[2]["pin"] == "claude" and rows[2]["ready"] is True, "a runner is matched by its command"
    assert rows[3] == {"name": "mine", "type": "command", "pin": None, "pinned": None, "ready": None, "why": None}


@pytest.mark.platforms("posix")
def test_hello_carries_the_runner_rows(world):
    from hermes_cli.worker import device
    (world["root"] / "config.yaml").write_text(
        "worker:\n  runners:\n    codex: {type: command, argv: [codex, exec]}\n", encoding="utf-8")
    assert _install(world, "codex") == 0

    code, output = device.dispatch(world["root"] / "worker", ["hello"])

    assert code == 0
    rows = {row["name"]: row for row in json.loads(output)["capabilities"]["runners"]}
    assert rows["codex"]["type"] == "command"
    assert (rows["codex"]["pinned"], rows["codex"]["installed"], rows["codex"]["ready"]) == ("1.0.0", "1.0.0", True)
    assert rows["codex"]["person_installed"]["version"] == "0.9.0" and rows["codex"]["sign_in"] == "magpie"


@pytest.mark.platforms("posix")
def test_an_acp_runner_is_pinned_by_its_adapter_before_its_name(world):
    world["table"]["claude-agent-acp"] = _npm("claude-agent-acp", "@zed-industries/claude-agent-acp", "0.23.1",
                                              sign_in="magpie", check="package")
    world["table"]["codex-acp"] = _npm("codex-acp", "@zed-industries/codex-acp", "0.16.0", sign_in="magpie",
                                       check="package")
    (world["root"] / "config.yaml").write_text(textwrap.dedent("""\
        worker:
          runners:
            claude: {type: acp, argv: [claude-agent-acp]}
            codex: {type: acp, argv: [/opt/zed/codex-acp, --stdio]}
            grok: {type: acp, argv: [/opt/x/grok-wrapper]}
            plain: {type: command, argv: [claude, -p]}
        """), encoding="utf-8")
    assert _install(world, "claude-agent-acp") == 0
    assert _install(world, "codex-acp") == 0

    rows = {row["name"]: row for row in lina_runners.node_report(world["root"] / "worker")}

    assert (rows["claude"]["pin"], rows["claude"]["pinned"], rows["claude"]["installed"]) == \
        ("claude-agent-acp", "0.23.1", "0.23.1"), "the adapter it runs, not the CLI that shares its name"
    assert rows["claude"]["ready"] is True
    assert (rows["codex"]["pin"], rows["codex"]["installed"]) == ("codex-acp", "0.16.0"), \
        "the command in argv decides, absolute path or not"
    assert rows["grok"]["pin"] == "grok", "a command name with no pin falls back to the runner's name"
    assert rows["plain"]["pin"] == "claude", "a command runner keeps the pin by name, then by its command"
    assert "pinned 0.23.1 · installed 0.23.1" in "\n".join(lina_runners.render([rows["claude"]])), \
        "`lina devices` shows the adapter's pinned and installed versions"



@pytest.mark.platforms("posix")
def test_a_runner_with_no_known_sign_in_check_shows_unknown_not_a_failure(world):
    world["table"]["dsh"] = _npm("dsh", "@deepseek-ai/dsh", "0.2.1-rc.5", sign_in="unknown")
    assert _install(world, "dsh") == 0

    row = {item["name"]: item for item in lina_runners.status(world["root"])}["dsh"]

    assert (row["sign_in"], row["sign_in_why"]) == (
        "unknown", "no read-only sign-in check is known for this runner"), "unknown is not shown as a failure"
    assert (row["installed"], row["ready"], row["why"]) == ("0.2.1-rc.5", True, None)
    line = "\n".join(lina_runners.render([row]))
    assert "sign-in unknown (no read-only sign-in check is known for this runner)" in line
    assert "not ready" not in line


@pytest.mark.platforms("posix")
def test_a_late_or_failing_probe_does_not_hold_hello(world, monkeypatch):
    import threading
    (world["root"] / "config.yaml").write_text(
        "worker:\n  runners:\n    codex: {type: command, argv: [codex]}\n    grok: {type: command, argv: [grok]}\n",
        encoding="utf-8")
    release = threading.Event()

    def hang(argv, **kwargs):
        release.wait(30)  # the probe never answers while the report is built
        raise subprocess.TimeoutExpired(argv, 30)

    try:
        rows = {row["name"]: row for row in lina_runners.node_report(world["root"] / "worker", run=hang, timeout=0.5)}
    finally:
        release.set()
    assert (rows["codex"]["why"], rows["codex"]["why_detail"]) == ("unknown", "no answer in 0.5s")
    assert rows["codex"]["pinned"] == "1.0.0" and rows["codex"]["ready"] is False

    def broken(root):
        raise RuntimeError("check broke")

    monkeypatch.setitem(lina_runners.SIGN_IN, "grok", broken)
    rows = {row["name"]: row for row in lina_runners.node_report(world["root"] / "worker")}
    assert rows["grok"]["why"] == "unknown" and "check broke" in rows["grok"]["why_detail"]
    assert rows["codex"]["why"] == "prepare", "one failing probe leaves the other rows alone"


@pytest.mark.platforms("posix")
def test_status_reads_the_active_install_not_the_link_name(world):
    assert _install(world) == 0
    root = world["root"]
    cursor = world["table"]["cursor-agent"]
    entry = lina_runners.entry_path(root, cursor)
    entry.write_text(f"#!{sys.executable}\nprint('2099.1.1')\n")  # updated itself in place, under the pinned dir

    rows = {row["name"]: row for row in lina_runners.status(root)}
    assert rows["cursor-agent"]["current"] == cursor.version
    assert (rows["cursor-agent"]["installed"], rows["cursor-agent"]["why"]) == ("2099.1.1", "update")

    manifest = lina_runners.vendor_dir(root, "codex") / "current" / "node_modules" / "@openai/codex" / "package.json"
    manifest.write_text(json.dumps({"name": "@openai/codex", "version": "1.0.9"}))
    (lina_runners.entry_path(root, world["table"]["grok"])).unlink()

    rows = {row["name"]: row for row in lina_runners.status(root)}
    assert (rows["codex"]["installed"], rows["codex"]["why"]) == ("1.0.9", "update")
    assert (rows["grok"]["installed"], rows["grok"]["why"]) == (None, "prepare")
    assert "missing" in rows["grok"]["installed_why"]


def test_the_shipped_pins_carry_a_version_and_integrity_for_every_runner():
    table = lina_runners.pins()
    assert {"codex", "claude", "grok", "dsh", "cursor-agent", "agy", "claude-agent-acp", "codex-acp"} <= set(table)
    for runner in table.values():
        assert runner.version
        if runner.installer == "npm":
            assert runner.package and runner.integrity.startswith("sha512-")
        else:
            assert runner.artifacts and all(art["url"].startswith("https://") and (art.get("sha256") or art.get("sha512"))
                                            for art in runner.artifacts.values())
        assert runner.sign_in in lina_runners.SIGN_IN


def test_hello_reads_the_runner_report_at_most_once_a_minute(world, monkeypatch):
    # Every hub contact asks hello first: the runner probes must not run on each of them.
    calls = []
    monkeypatch.setattr(lina_runners, "node_report", lambda root: calls.append(root) or [{"name": "codex"}])
    worker_root = world["root"] / "worker"
    assert lina_runners.cached_node_report(worker_root) == [{"name": "codex"}]
    assert lina_runners.cached_node_report(worker_root) == [{"name": "codex"}]
    assert len(calls) == 1
    assert lina_runners.cached_node_report(worker_root, max_age=0) == [{"name": "codex"}]
    assert len(calls) == 2


@pytest.mark.platforms("posix")
@pytest.mark.parametrize(("health", "models", "signed", "ready", "reason"), [
    (200, 2, "magpie", True, None),
    (503, 0, "unknown", False, "model path does not answer"),
    (200, 0, "unknown", False, "model path lists no model"),
])
def test_magpie_readiness_requires_a_successful_nonempty_model_list(world, health, models, signed, ready, reason):
    assert _install(world, "codex") == 0
    world["proxy"].update(health=health, models=models)

    row = lina_runners.row(world["root"], world["table"]["codex"])

    assert (row["sign_in"], row["ready"]) == (signed, ready)
    if reason is None:
        assert row["sign_in_why"] is None and row["why"] is None
    else:
        assert reason in row["sign_in_why"] and row["why"] == "prepare"
