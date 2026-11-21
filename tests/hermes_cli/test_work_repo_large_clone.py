"""Opt-in local Git benchmark. Run with HERMES_RUN_E2E=1 python -B <this file>."""

import hashlib
import json
import os
import shlex
import sqlite3
import subprocess
import sys
import tempfile
from contextlib import closing
from dataclasses import dataclass
from pathlib import Path
from time import perf_counter
from typing import Final

PAYLOAD_BYTES: Final = 1_000_000_000
SPARSE_OBJECT_LIMIT: Final = 1_048_576
README: Final = b"# Local clone benchmark\n"
AUDIO: Final = Path("audio/random.wav")
OPT_IN: Final = "HERMES_RUN_E2E"
CHECKOUT: Final = Path(__file__).resolve().parents[2]


@dataclass(frozen=True, slots=True)
class _Sandbox:
    root: Path

    def run(self, command: list[str], cwd: Path, stdin: str | None = None) -> str:
        print(f"$ {shlex.join(command)}", flush=True)
        result = subprocess.run(
            command,
            cwd=cwd,
            input=stdin,
            capture_output=True,
            text=True,
            encoding="utf-8",
            timeout=300,
            env={
                "PATH": os.pathsep.join((str(Path(sys.executable).parent), os.environ["PATH"])),
                "HOME": str(self.root),
                "HERMES_HOME": str(self.root / ".lina"),
                "LINA_HOME": str(self.root / ".lina"),
                "HERMES_RUNTIME_DIR": str(self.root / "runtime"),
                "XDG_CONFIG_HOME": str(self.root / "config"),
                "XDG_CACHE_HOME": str(self.root / "cache"),
                "TMPDIR": str(self.root),
                "PYTHONDONTWRITEBYTECODE": "1",
                "PYTHONUTF8": "1",
                "GIT_CONFIG_GLOBAL": os.devnull,
                "GIT_CONFIG_NOSYSTEM": "1",
                "GIT_ALLOW_PROTOCOL": "file",
                "LC_ALL": "C",
            },
            check=False,
        )
        if result.stderr:
            print(result.stderr, end="", flush=True)
        result.check_returncode()
        return result.stdout.strip()

    def git(self, *args: str, cwd: Path | None = None, stdin: str | None = None) -> str:
        return self.run(["git", "-C", str(cwd or self.root), *args], self.root, stdin)

    def create_registered_remote(self) -> Path:
        """Create A1, then run the real repo-create launcher with a local forge."""
        home = self.root / ".lina"
        home.mkdir()
        forge = self.root / "forge"
        remote = forge / "A1" / "album.git"
        remote.parent.mkdir(parents=True)
        desk = self.root / "Desk"
        desk.mkdir()
        (home / "config.yaml").write_text(
            json.dumps({
                "desk": {"path": str(desk)},
                "work": {
                    "owner": {"id": "owner", "name": "Owner"},
                    "agents": {"alpha": "Alpha"},
                    "store": {"backend": "sqlite"},
                    "forge": {
                        "url": f"{forge.as_uri()}/{{repo}}.git",
                        "create_command": [
                            sys.executable, "-c",
                            "import pathlib, subprocess, sys\n"
                            "remote = pathlib.Path(sys.argv[1])\n"
                            "if remote.as_uri() != sys.argv[2]:\n"
                            "    sys.exit('forge URL does not match the requested repository')\n"
                            "subprocess.run(['git', 'init', '--bare', '--initial-branch=main', "
                            "'--template=', str(remote)], check=True)\n",
                            str(forge / "{repo}.git"), "{url}",
                        ],
                    },
                },
            }),
            encoding="utf-8",
        )
        self.run(
            [
                "./lina", "work", "add", "목표", "--by", "alpha", "--area", "Media",
                "--title", "Archive audio", "--basis", "Keep a copy",
                "--done", "Audio is archived", "--check", "false",
            ],
            CHECKOUT,
        )
        print(self.run(["./lina", "repo", "create", "A1/album"], CHECKOUT), flush=True)
        store = home / "work" / "work.db"
        with closing(sqlite3.connect(f"{store.as_uri()}?mode=ro", uri=True)) as db:
            row = db.execute(
                "SELECT id, area_id, name, forge_url FROM repo WHERE id = ?",
                ("A1/album",),
            ).fetchone()
        assert row == ("A1/album", "A1", "album", remote.as_uri())
        print(f"verified registry entry: {row}", flush=True)
        self.git("config", "uploadpack.allowFilter", "true", cwd=remote)
        return remote

    def push_payload(self, remote: Path) -> str:
        """Generate the original bytes and push them through file transport."""
        seed = self.root / "seed.git"
        self.git("init", "--bare", "--initial-branch=main", "--template=", str(seed))
        source = self.root / AUDIO.name
        digest = hashlib.sha256()
        with source.open("wb") as stream:
            for offset in range(0, PAYLOAD_BYTES, 1_000_000):
                chunk = os.urandom(min(1_000_000, PAYLOAD_BYTES - offset))
                stream.write(chunk)
                digest.update(chunk)
        audio_oid = self.git("hash-object", "-w", str(source), cwd=seed)
        source.unlink()
        readme_oid = self.git(
            "hash-object", "-w", "--stdin", cwd=seed, stdin=README.decode("ascii")
        )
        audio_tree = self.git(
            "mktree", cwd=seed, stdin=f"100644 blob {audio_oid}\t{AUDIO.name}\n"
        )
        root_tree = self.git(
            "mktree",
            cwd=seed,
            stdin=f"100644 blob {readme_oid}\tREADME.md\n040000 tree {audio_tree}\taudio\n",
        )
        tip = self.git(
            "-c", "user.name=Clone Fixture", "-c", "user.email=fixture@example.invalid",
            "commit-tree", root_tree, "-m", "Synthetic clone fixture", cwd=seed,
        )
        self.git("push", remote.as_uri(), f"{tip}:refs/heads/main", cwd=seed)
        return digest.hexdigest()

    def compare(self, remote: Path, expected_sha256: str) -> None:
        """Compare against a bare fixture with the same paths and known audio digest."""
        assert self.git("rev-parse", "--is-bare-repository", cwd=remote) == "true"
        url = remote.resolve().as_uri()
        audio_oid = self.git("rev-parse", f"HEAD:{AUDIO.as_posix()}", cwd=remote)
        full = self.root / "full"
        sparse = self.root / "sparse"

        started = perf_counter()
        self.git("clone", url, str(full))
        full_seconds = perf_counter() - started
        full_audio_bytes = (full / AUDIO).stat().st_size
        full_object_bytes = _object_bytes(full)
        assert full_audio_bytes == PAYLOAD_BYTES
        assert (full / "README.md").read_bytes() == README
        print(
            f"full clone: {full_seconds:.6f} s, {full_object_bytes} object bytes, "
            f"{full_audio_bytes + len(README)} worktree file bytes",
            flush=True,
        )

        started = perf_counter()
        self.git("clone", "--filter=blob:none", "--sparse", url, str(sparse))
        sparse_seconds = perf_counter() - started
        assert (sparse / "README.md").read_bytes() == README
        assert not (sparse / AUDIO).exists()
        assert self.git("status", "--porcelain", cwd=sparse) == ""
        objects = self.git("rev-list", "--objects", "--all", "--missing=print", cwd=sparse)
        assert f"?{audio_oid}" in objects.splitlines(), "audio blob was downloaded eagerly"
        sparse_object_bytes = _object_bytes(sparse)
        assert sparse_object_bytes <= SPARSE_OBJECT_LIMIT, sparse_object_bytes
        print(
            f"sparse clone: {sparse_seconds:.6f} s, {sparse_object_bytes} object bytes, "
            f"{len(README)} worktree file bytes",
            flush=True,
        )

        started = perf_counter()
        self.git("sparse-checkout", "add", AUDIO.parent.as_posix(), cwd=sparse)
        fetch_seconds = perf_counter() - started
        fetched_audio_bytes = (sparse / AUDIO).stat().st_size
        assert fetched_audio_bytes == PAYLOAD_BYTES
        with (sparse / AUDIO).open("rb") as stream:
            fetched_sha256 = hashlib.file_digest(stream, "sha256").hexdigest()
        assert fetched_sha256 == expected_sha256
        print(
            f"on-demand checkout: {fetch_seconds:.6f} s, {_object_bytes(sparse)} object bytes, "
            f"{fetched_audio_bytes + len(README)} worktree file bytes",
            flush=True,
        )
        print(f"verified audio SHA-256: {fetched_sha256}", flush=True)


def _object_bytes(clone: Path) -> int:
    return sum(
        path.stat().st_size for path in (clone / ".git" / "objects").rglob("*") if path.is_file()
    )


def _exercise(tmp_path: Path) -> None:
    with tempfile.TemporaryDirectory(prefix="large-clone-", dir=tmp_path) as temp:
        sandbox = _Sandbox(Path(temp))
        print(sandbox.git("--version"), flush=True)
        remote = sandbox.create_registered_remote()
        digest = sandbox.push_payload(remote)
        sandbox.compare(remote, digest)


def test_sparse_partial_clone_defers_random_audio(tmp_path: Path) -> None:
    if os.environ.get(OPT_IN) != "1":
        import pytest

        pytest.skip(f"set {OPT_IN}=1 to run the 1 GB Git benchmark")
    _exercise(tmp_path)


if __name__ == "__main__":
    if os.environ.get(OPT_IN) != "1":
        raise SystemExit(f"set {OPT_IN}=1 to run the 1 GB Git benchmark")
    out = CHECKOUT / "out"
    assert out.resolve() == out, "benchmark out/ must be inside the checkout"
    out.mkdir(exist_ok=True)
    temp_parent = Path(os.environ.get("TMPDIR", str(out))).resolve()
    _exercise(temp_parent if temp_parent.is_relative_to(out) else out)
