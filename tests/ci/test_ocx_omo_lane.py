"""lina#34: CI builds and tests ``tools/ocx-omo`` on the Node pinned in ``pm/lina-deps.json``.

The job is read from ``ci.yml`` and its Node step is run for real against a temp checkout (a fake ``curl`` serves the
tarball): it installs the Node whose sha256 the pin names and refuses any other.
"""

from __future__ import annotations

import hashlib
import io
import json
import os
import subprocess
import sys
import tarfile
from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[2]


def _job() -> dict:
    yaml = pytest.importorskip("hermes_yaml")
    jobs = yaml.safe_load((ROOT / ".github/workflows/ci.yml").read_text(encoding="utf-8-sig"))["jobs"]
    found = [body for body in jobs.values() if any(step.get("working-directory") == "tools/ocx-omo"
                                                   for step in body.get("steps", []))]
    assert len(found) == 1, "one ci.yml job runs in tools/ocx-omo"
    return found[0]


def test_a_ci_job_builds_and_tests_ocx_omo_on_every_change():
    job = _job()
    assert "if" not in job and "needs" not in job  # not gated on a lane: it runs for every change
    run = next(step["run"] for step in job["steps"] if step.get("working-directory") == "tools/ocx-omo")
    lines = [line.strip() for line in run.splitlines()]
    assert lines.index("npm ci --ignore-scripts") < lines.index("npm run build") < lines.index("npm test")


def _node_tarball(version: str) -> bytes:
    buf = io.BytesIO()
    with tarfile.open(fileobj=buf, mode="w:gz") as archive:
        data = b"#!/bin/sh\necho v" + version.encode() + b"\n"
        info = tarfile.TarInfo(f"node-v{version}-linux-x64/bin/node")
        info.size, info.mode = len(data), 0o755
        archive.addfile(info, io.BytesIO(data))
    return buf.getvalue()


@pytest.mark.platforms("linux")
@pytest.mark.parametrize("tampered", [False, True])
def test_the_node_step_installs_only_the_pinned_tarball(tmp_path, tampered):
    step = next(step for step in _job()["steps"] if "lina-deps.json" in step.get("run", ""))
    tarball = _node_tarball("24.0.0")
    (tmp_path / "pm").mkdir()
    (tmp_path / "pm" / "lina-deps.json").write_text(json.dumps({"node": {
        "version": "24.0.0", "dist": "https://nodejs.invalid/dist",
        "sha256": {"linux-x64": hashlib.sha256(tarball).hexdigest()}}}))
    served = tmp_path / "served.tar.gz"
    served.write_bytes(tarball + (b"tampered" if tampered else b""))
    bin_dir = tmp_path / "fakebin"
    bin_dir.mkdir()
    curl = bin_dir / "curl"
    curl.write_text(f'#!{sys.executable}\nimport shutil, sys\nshutil.copy({str(served)!r}, '
                    'sys.argv[sys.argv.index("-o") + 1])\n')
    curl.chmod(0o755)
    runner_temp, github_path = tmp_path / "runner", tmp_path / "github_path"
    runner_temp.mkdir()
    github_path.write_text("")
    result = subprocess.run(["bash", "-c", step["run"]], cwd=tmp_path, capture_output=True, text=True, timeout=60,
                            env={**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}",
                                 "RUNNER_TEMP": str(runner_temp), "GITHUB_PATH": str(github_path)})
    if tampered:
        assert result.returncode != 0 and github_path.read_text() == ""
        assert not (runner_temp / "node-v24.0.0-linux-x64").exists()
        return
    assert result.returncode == 0, result.stderr
    added = github_path.read_text().strip()
    assert added == str(runner_temp / "node-v24.0.0-linux-x64" / "bin")
    assert subprocess.run([f"{added}/node"], capture_output=True, text=True).stdout.strip() == "v24.0.0"
