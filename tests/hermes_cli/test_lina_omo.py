"""LINA's pinned omo (lina#34): installed with the pinned Node's npm under the LINA home only, the lock must carry
the pinned integrity, ``bin/omo`` runs ``current`` on the pinned Node, upgrade and rollback switch ``current``
without touching a running daemon, and ``lina omo routing`` passes ocx-omo's exit code through.

The fake Node is a Python program: for ``npm-cli.js`` it does what npm would (``install`` writes the lock and an
``omo.js``; ``run build`` writes ocx-omo's ``dist/src/cli.js``), for any other script it runs that script as
Python, so the fake ``omo.js`` and ``cli.js`` record how they were started.
"""
import argparse
import hashlib
import io
import json
import os
import subprocess
import sys
import tarfile
from pathlib import Path

import pytest

from hermes_cli import lina_ocx, lina_omo, lina_omo_routing
from hermes_cli.subcommands.omo import build_omo_parser

INTEGRITY = "sha512-" + "A" * 86 + "=="
PINNED = "5.1.51"

_FAKE_OMO = '''import json, os, sys
with open(os.environ["FAKE_LOG"], "a") as fh:
    fh.write(json.dumps(sys.argv[1:]) + "\\n")
if sys.argv[1:] == ["--version"]:
    print("omo %s (engine: fake)" % ("0.0.0" if os.environ.get("FAKE_WRONG_VERSION") else {version!r}))
elif sys.argv[1:3] == ["host", "status"]:
    print(os.environ.get("FAKE_DAEMON", '{{"reachable": false}}'))
else:
    print(json.dumps({{"argv": sys.argv[1:], "node": os.environ["FAKE_NODE"],
                      "runtime": os.environ.get("OMO_RUNTIME")}}))
'''
_FAKE_CLI = '''import json, os, sys
with open(os.environ["FAKE_LOG"], "a") as fh:
    fh.write(json.dumps(["ocx-omo", *sys.argv[1:]]) + "\\n")
print("ocx-omo " + sys.argv[1])
sys.exit(json.loads(os.environ.get("FAKE_CODES", "{}")).get(sys.argv[1], 0))
'''
_FAKE_NODE = '''#!{python}
import json, os, runpy, sys
script, args = sys.argv[1], sys.argv[2:]
if script.endswith("npm-cli.js"):
    open(os.path.join(os.environ["HOME"], "npm-was-here"), "w").close()
    if args[0] == "install":
        prefix = args[args.index("--prefix") + 1]
        name, version = args[-1].rsplit("@", 1)
        os.makedirs(os.path.join(prefix, "node_modules", name, "bin"))
        with open(os.path.join(prefix, "node_modules", name, "bin", "omo.js"), "w") as fh:
            fh.write({omo!r}.format(version=version))
        lock = {{"packages": {{"node_modules/" + name: {{"version": version, "integrity": {integrity!r}}}}}}}
        with open(os.path.join(prefix, "package-lock.json"), "w") as fh:
            json.dump(lock, fh)
    elif args == ["run", "build"]:
        os.makedirs(os.path.join("dist", "src"))
        with open(os.path.join("dist", "src", "cli.js"), "w") as fh:
            fh.write({cli!r})
    sys.exit(0)
os.environ["FAKE_NODE"] = sys.argv[0]
sys.argv = sys.argv[1:]
runpy.run_path(script, run_name="__main__")
'''


def _node_tarball(version: str) -> bytes:
    node = _FAKE_NODE.format(python=sys.executable, omo=_FAKE_OMO, cli=_FAKE_CLI, integrity=INTEGRITY)
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as archive:
        for path, text, mode in ((f"node-v{version}/bin/node", node, 0o755),
                                 (f"node-v{version}/lib/node_modules/npm/bin/npm-cli.js", "// npm\n", 0o644)):
            data = text.encode()
            info = tarfile.TarInfo(path)
            info.size, info.mode = len(data), mode
            archive.addfile(info, io.BytesIO(data))
    return buf.getvalue()


@pytest.fixture
def world(tmp_path, monkeypatch):
    """An account home holding an omo installed by hand, and a LINA root inside it; nothing reaches the network."""
    for key in [key for key in os.environ if key.startswith(("OMO_", "SENPI_"))]:
        monkeypatch.delenv(key)
    account = tmp_path / "acct"
    root = account / ".lina"
    root.mkdir(parents=True)
    own = account / ".bun" / "install" / "global" / "node_modules" / "omo-ai" / "package.json"
    own.parent.mkdir(parents=True)
    own.write_text('{"name": "omo-ai", "version": "5.0.0"}\n')
    monkeypatch.setenv("HOME", str(account))
    log = tmp_path / "calls.jsonl"
    monkeypatch.setenv("FAKE_LOG", str(log))
    real = lina_ocx.pins()
    tarball = _node_tarball(real.node_version)
    digest = hashlib.sha256(tarball).hexdigest()
    monkeypatch.setattr(lina_ocx, "pins", lambda path=None: lina_ocx.Pins(
        real.node_version, "https://nodejs.invalid/dist", {key: digest for key in real.node_sha256}, real.package,
        real.version, real.integrity))
    monkeypatch.setattr(lina_omo, "omo_pin", lambda path=None: lina_omo.OmoPin("omo-ai", PINNED, INTEGRITY))
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: root)

    def fetch(url, dest):
        Path(dest).write_bytes(tarball)

    def calls():
        return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []

    return {"tmp": tmp_path, "root": root, "fetch": fetch, "calls": calls, "own": own}


def _install(world) -> None:
    assert lina_omo.install(world["root"], fetch=world["fetch"], out=lambda line: None) == 0


def _lina_omo(*argv: str) -> int:
    parser = argparse.ArgumentParser()
    build_omo_parser(parser.add_subparsers())
    args = parser.parse_args(["omo", *argv])
    with pytest.raises(SystemExit) as exited:
        args.func(args)
    return exited.value.code


@pytest.mark.platforms("posix")
def test_install_writes_only_under_the_lina_home_and_the_shim_runs_current_on_the_pinned_node(world):
    before = {path: path.stat().st_mtime_ns for path in world["tmp"].rglob("*")}
    _install(world)
    root = world["root"]
    written = {path for path in world["tmp"].rglob("*") if path not in before} - {world["tmp"] / "calls.jsonl"}
    assert written and all(path.is_relative_to(root) for path in written)
    assert world["own"].read_text() == '{"name": "omo-ai", "version": "5.0.0"}\n'  # the person's omo is untouched
    lock = json.loads((root / "vendor" / "omo" / PINNED / "package-lock.json").read_text())
    assert lock["packages"]["node_modules/omo-ai"] == {"version": PINNED, "integrity": INTEGRITY}
    assert lina_omo.current_version(root) == PINNED
    assert lina_omo_routing.cli_path(root).exists()

    shown = subprocess.run([str(root / "bin" / "omo"), "run", "--flag"], capture_output=True, text=True, check=True,
                           env={"PATH": "/usr/bin:/bin", "HOME": str(world["tmp"] / "acct"),
                                "FAKE_LOG": str(world["tmp"] / "calls.jsonl")})
    assert json.loads(shown.stdout) == {"argv": ["run", "--flag"], "node": str(lina_omo.node_bin(root)),
                                        "runtime": "node"}


@pytest.mark.platforms("posix")
def test_an_omo_whose_lock_has_another_integrity_is_refused_and_nothing_is_kept(world, monkeypatch):
    monkeypatch.setattr(lina_omo, "omo_pin", lambda path=None: lina_omo.OmoPin("omo-ai", PINNED,
                                                                               "sha512-" + "B" * 86 + "=="))
    with pytest.raises(lina_ocx.OcxError, match="the pin is sha512-B"):
        lina_omo.install(world["root"], fetch=world["fetch"], out=lambda line: None)
    assert not (world["root"] / "vendor" / "omo" / PINNED).exists()
    assert lina_omo.current_version(world["root"]) is None
    assert not lina_omo.shim_path(world["root"]).exists()


@pytest.mark.platforms("posix")
def test_upgrade_and_rollback_switch_current_and_leave_a_running_daemon_alone(world, monkeypatch):
    _install(world)
    root, lines = world["root"], []
    monkeypatch.setenv("FAKE_DAEMON", '{"reachable": true, "pid": 4242}')
    assert lina_omo.upgrade(root, "5.1.60", INTEGRITY, fetch=world["fetch"], out=lines.append) == 0
    assert lina_omo.current_version(root) == "5.1.60"
    assert f"{lina_omo.shim_path(root)} host handoff" in lines[-1]
    assert lina_omo.rollback(root, out=lines.append) == 0
    assert lina_omo.current_version(root) == PINNED
    assert lina_omo.rollback(root, out=lines.append) == 1  # one upgrade, one rollback
    omo_calls = [call for call in world["calls"]() if call[:1] != ["ocx-omo"]]
    assert omo_calls and all(call in (["--version"], ["host", "status", "--json"]) for call in omo_calls)


@pytest.mark.platforms("posix")
def test_an_upgrade_whose_omo_reports_another_version_keeps_current(world, monkeypatch):
    _install(world)
    monkeypatch.setenv("FAKE_WRONG_VERSION", "1")
    lines = []
    assert lina_omo.upgrade(world["root"], "5.1.60", INTEGRITY, fetch=world["fetch"], out=lines.append) == 1
    assert lina_omo.current_version(world["root"]) == PINNED
    assert "reports 0.0.0" in lines[-1]


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("code", [0, 10])
def test_lina_omo_routing_check_passes_ocx_omo_s_exit_code_through(world, monkeypatch, code):
    _install(world)
    monkeypatch.setenv("FAKE_CODES", json.dumps({"check": code}))
    assert _lina_omo("routing", "check", "--json") == code
    assert world["calls"]()[-1] == ["ocx-omo", "check", "--json", "--omo-task",
                                    str(lina_omo.omo_task_path(world["root"]))]


@pytest.mark.platforms("posix")
def test_install_shows_pending_routing_and_applies_it_only_with_apply(world, monkeypatch):
    monkeypatch.setenv("FAKE_CODES", json.dumps({"check": 10}))
    quiet = {"root": world["root"], "fetch": world["fetch"], "out": lambda line: None}
    assert lina_omo_routing.install_step(**quiet) == 0
    verbs = [call[1] for call in world["calls"]() if call[:1] == ["ocx-omo"]]
    assert verbs == ["check", "plan"]
    assert lina_omo_routing.install_step(apply=True, **quiet) == 0
    verbs = [call[1] for call in world["calls"]() if call[:1] == ["ocx-omo"]]
    assert verbs == ["check", "plan", "check", "apply"]
