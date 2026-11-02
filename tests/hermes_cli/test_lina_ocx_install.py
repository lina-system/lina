"""``lina models install`` (lina#32): the pinned Node is unpacked only after its checksum matched, OCX is installed
with that Node's npm under the LINA home, the lock must carry the pinned integrity, and nothing is written outside
the LINA home."""
import hashlib
import io
import json
import sys
import tarfile
from pathlib import Path

import pytest

from hermes_cli import lina_ocx

INTEGRITY = "sha512-" + "A" * 86 + "=="

# A Node whose ``node`` runs npm-cli.js the way LINA calls npm, logging each call to $HOME/npm-calls.jsonl:
# ``install --package-lock-only`` writes the lock npm would (the integrity in this script), ``ci`` lays out the
# locked tree, ``rebuild`` runs the install scripts (bun's postinstall touches $HOME).
_FAKE_NODE = '''#!{python}
import json, os, sys
args = sys.argv[2:]
home = os.environ["HOME"]
with open(os.path.join(home, "npm-calls.jsonl"), "a") as fh:
    fh.write(json.dumps(args) + "\\n")
if args[0] == "install" and "--package-lock-only" in args:
    (name, version), = json.load(open("package.json"))["dependencies"].items()
    lock = {{"lockfileVersion": 3, "packages": {{"": {{"dependencies": {{name: version}}}},
            "node_modules/" + name: {{"version": version, "integrity": {integrity!r}}}}}}}
    with open("package-lock.json", "w") as fh:
        json.dump(lock, fh)
elif args[0] == "ci":
    os.makedirs(os.path.join("node_modules", "bun", "bin"), exist_ok=True)
    open(os.path.join("node_modules", "bun", "bin", "bun.exe"), "w").close()
elif args[0] == "rebuild":
    open(os.path.join(home, "npm-was-here"), "w").close()
else:
    sys.exit("unexpected npm call: " + " ".join(args))
'''


def _node_tarball(version: str, integrity: str) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as archive:
        for path, text, mode in ((f"node-v{version}/bin/node", _FAKE_NODE.format(python=sys.executable,
                                                                                    integrity=integrity), 0o755),
                                 (f"node-v{version}/lib/node_modules/npm/bin/npm-cli.js", "// npm\n", 0o644)):
            data = text.encode()
            info = tarfile.TarInfo(path)
            info.size, info.mode = len(data), mode
            archive.addfile(info, io.BytesIO(data))
    return buf.getvalue()


@pytest.fixture
def world(tmp_path, monkeypatch):
    """An account home and a LINA root inside it; downloads come from *tarball*, keyed by the pinned checksum."""
    account = tmp_path / "acct"
    root = account / ".lina"
    root.mkdir(parents=True)
    monkeypatch.setenv("HOME", str(account))
    real = lina_ocx.pins()
    tarball = _node_tarball(real.node_version, INTEGRITY)
    digest = hashlib.sha256(tarball).hexdigest()
    pin = lina_ocx.Pins(real.node_version, "https://nodejs.invalid/dist", {key: digest for key in real.node_sha256},
                        real.package, real.version, INTEGRITY)
    monkeypatch.setattr(lina_ocx, "pins", lambda path=None: pin)
    monkeypatch.setattr(lina_ocx, "LOCK_DIR", tmp_path / "npm-locks")  # no committed lock unless a test writes one
    fetched = []

    def fetch(url, dest):
        fetched.append(url)
        Path(dest).write_bytes(tarball)

    return {"tmp": tmp_path, "account": account, "root": root, "pin": pin, "fetch": fetch, "fetched": fetched}


def _files(base: Path) -> set[Path]:
    return {path for path in base.rglob("*")}


@pytest.mark.platforms("posix")
def test_install_puts_node_and_ocx_under_the_lina_home_only(world, capsys):
    before = _files(world["tmp"])
    assert lina_ocx.install(world["root"], fetch=world["fetch"]) == 0
    root, pin = world["root"], world["pin"]
    written = _files(world["tmp"]) - before
    assert written and all(path.is_relative_to(root) for path in written)
    assert world["fetched"] == [f"https://nodejs.invalid/dist/v{pin.node_version}/"
                                f"node-v{pin.node_version}-{lina_ocx.platform_key()}.tar.gz"]
    assert (root / "runtime" / f"node-{pin.node_version}" / "bin" / "node").exists()
    lock = json.loads((root / "vendor" / "ocx" / pin.version / "package-lock.json").read_text())
    assert lock["packages"][f"node_modules/{pin.package}"] == {"version": pin.version, "integrity": INTEGRITY}
    assert lina_ocx.current_version(root) == pin.version
    assert (root / "vendor" / "ocx" / "home" / "npm-was-here").exists()  # npm ran with LINA's HOME, not the account's
    assert (root / "ocx").is_dir()


@pytest.mark.platforms("posix")
def test_a_node_tarball_with_another_checksum_is_refused_and_nothing_is_unpacked(world):
    def tampered(url, dest):
        Path(dest).write_bytes(b"not the pinned node")

    with pytest.raises(lina_ocx.OcxError, match="checksum mismatch"):
        lina_ocx.install(world["root"], fetch=tampered)
    assert not (world["root"] / "runtime" / f"node-{world['pin'].node_version}").exists()
    assert not (world["root"] / "vendor").exists()


@pytest.mark.platforms("posix")
def test_ocx_whose_lock_has_another_integrity_is_not_kept(world, monkeypatch):
    pin = world["pin"]
    other = lina_ocx.Pins(pin.node_version, pin.node_dist, pin.node_sha256, pin.package, pin.version,
                          "sha512-" + "B" * 86 + "==")
    monkeypatch.setattr(lina_ocx, "pins", lambda path=None: other)
    with pytest.raises(lina_ocx.OcxError, match="the pin is sha512-B"):
        lina_ocx.install(world["root"], fetch=world["fetch"])
    assert not (world["root"] / "vendor" / "ocx" / pin.version).exists()
    assert lina_ocx.current_version(world["root"]) is None


def test_the_port_comes_from_ocx_port_in_the_lina_config(tmp_path):
    assert lina_ocx.configured_port(tmp_path) == lina_ocx.DEFAULT_PORT == 10100
    (tmp_path / "config.yaml").write_text("ocx:\n  port: 10150\n")
    assert lina_ocx.configured_port(tmp_path) == 10150


def _npm_calls(root: Path) -> list[list[str]]:
    log = root / "vendor" / "ocx" / "home" / "npm-calls.jsonl"
    return [json.loads(line) for line in log.read_text().splitlines()] if log.exists() else []


@pytest.mark.platforms("posix")
def test_no_package_script_runs_before_the_locked_tree_is_verified(world):
    assert lina_ocx.install(world["root"], fetch=world["fetch"]) == 0
    calls = _npm_calls(world["root"])
    assert [call[0] for call in calls] == ["install", "ci", "rebuild"]
    assert all("--ignore-scripts" in call for call in calls[:2])


@pytest.mark.platforms("posix")
def test_an_integrity_mismatch_runs_no_package_and_no_script(world, monkeypatch):
    pin = world["pin"]
    monkeypatch.setattr(lina_ocx, "pins", lambda path=None: lina_ocx.Pins(
        pin.node_version, pin.node_dist, pin.node_sha256, pin.package, pin.version, "sha512-" + "B" * 86 + "=="))
    with pytest.raises(lina_ocx.OcxError, match="nothing kept"):
        lina_ocx.install(world["root"], fetch=world["fetch"])
    assert [call[0] for call in _npm_calls(world["root"])] == ["install"]  # the lock only; no ci, no rebuild
    assert not (world["root"] / "vendor" / "ocx" / "home" / "npm-was-here").exists()


@pytest.mark.platforms("posix")
@pytest.mark.parametrize("locked", [INTEGRITY, "sha512-" + "B" * 86 + "=="])
def test_a_committed_lock_is_installed_as_is_and_must_carry_the_pin(world, locked):
    pin, lock = world["pin"], world["tmp"] / "npm-locks" / "ocx"
    lock.mkdir(parents=True)
    (lock / "package.json").write_text(json.dumps({"name": "lina-ocx", "dependencies": {pin.package: pin.version}}))
    (lock / "package-lock.json").write_text(json.dumps({"packages": {
        f"node_modules/{pin.package}": {"version": pin.version, "integrity": locked}}}))
    if locked != INTEGRITY:
        with pytest.raises(lina_ocx.OcxError, match="nothing kept"):
            lina_ocx.install(world["root"], fetch=world["fetch"])
        assert _npm_calls(world["root"]) == []
        return
    assert lina_ocx.install(world["root"], fetch=world["fetch"]) == 0
    assert [call[0] for call in _npm_calls(world["root"])] == ["ci", "rebuild"]


@pytest.mark.parametrize("name", ["ocx", "omo"])
def test_the_committed_lock_of_each_npm_pin_carries_the_pinned_integrity(name):
    pin = json.loads(lina_ocx.PIN_FILE.read_text())[name]
    lock = lina_ocx.committed_lock(name, pin["package"], pin["version"])
    assert lock is not None, f"pm/npm-locks/{name} does not lock {pin['package']}@{pin['version']}"
    entry = json.loads((lock / "package-lock.json").read_text())["packages"][f"node_modules/{pin['package']}"]
    assert (entry["version"], entry["integrity"]) == (pin["version"], pin["integrity"])
