"""``transport.fetch`` replaces the run's results instead of layering them (review of lina#35): a follow-up that
drops ``report.md`` must not leave the earlier run's report in the hub's result folder, where ``verify`` (which reads
only the files the new manifest lists) would pass and ``lina worker result`` would show the old report as current."""

from __future__ import annotations

import io
import tarfile
from pathlib import Path

from hermes_cli.worker import transport
from hermes_cli.worker.transport import PACKED, Reply


def _tar(files: dict[str, str]) -> bytes:
    buffer = io.BytesIO()
    with tarfile.open(fileobj=buffer, mode="w") as archive:
        for name, text in files.items():
            data = text.encode("utf-8")
            info = tarfile.TarInfo(name)
            info.size = len(data)
            archive.addfile(info, io.BytesIO(data))
    return buffer.getvalue()


class _Packs:
    def __init__(self, *packs: bytes):
        self.packs = list(packs)

    def call(self, verb: list[str], stdin: bytes = b"", timeout: float = 120) -> Reply:
        assert verb[0] == "pack"
        return Reply(0, self.packs.pop(0), "")


def test_a_deliverable_the_new_run_dropped_does_not_survive_from_the_earlier_fetch(tmp_path: Path):
    dest = tmp_path / "result"
    (dest / "notes.txt").parent.mkdir(parents=True)
    (dest / "notes.txt").write_text("hub's own file\n")
    first = _tar({"report.md": "run 1 report\n", "final.md": "one\n", "out/a.txt": "a\n", "git.txt": "g1\n"})
    second = _tar({"final.md": "two\n", "out/b.txt": "b\n"})
    pipe = _Packs(first, second)

    transport.fetch(pipe, "OT-1", dest)
    assert (dest / "report.md").read_text() == "run 1 report\n"

    transport.fetch(pipe, "OT-1", dest)
    assert not (dest / "report.md").exists()
    assert not (dest / "git.txt").exists()
    assert not (dest / "out" / "a.txt").exists()
    assert (dest / "final.md").read_text() == "two\n" and (dest / "out" / "b.txt").read_text() == "b\n"
    assert (dest / "notes.txt").read_text() == "hub's own file\n"  # only the names a pack can carry are replaced


def test_every_name_the_device_packs_is_cleared_before_a_fetch(tmp_path: Path, monkeypatch):
    from hermes_cli.worker import device

    task = tmp_path / "task"
    work = task / "work"
    (work / "out").mkdir(parents=True)
    for name in ("report.md",):
        (work / name).write_text("x\n")
    for name in ("final.md", "manifest.json", "git.txt", "stderr.1.txt"):
        (task / name).write_text("x\n")

    class _Task:
        def __init__(self):
            self.work = work

        def path(self, rel: str) -> Path:
            return task / rel

    monkeypatch.setattr(device, "task_of", lambda root, task_id: _Task())
    monkeypatch.setattr(device, "last_run", lambda t: 1)
    with tarfile.open(fileobj=io.BytesIO(device.pack(tmp_path, "OT-1")), mode="r") as archive:
        top = {name.split("/", 1)[0] for name in archive.getnames()}
    assert top == set(PACKED)
