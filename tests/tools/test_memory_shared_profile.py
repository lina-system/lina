"""lina#467: the shared owner profile layer of the built-in memory store, through the memory tool."""

import json
import os
import subprocess
import sys
from pathlib import Path

import pytest

from tools.memory_tool import MEMORY_LAYER_LABELS, MemoryStore, get_shared_memory_dir, memory_tool

REPO = Path(__file__).resolve().parents[2]


@pytest.fixture()
def install(tmp_path, monkeypatch):
    """An install root with three agent profiles; returns a function that makes one the active profile."""
    root = tmp_path / "install"
    (root / "profiles").mkdir(parents=True)
    (root / "config.yaml").write_text("{}\n")
    for name in ("a", "b", "c"):
        (root / "profiles" / name).mkdir()
        (root / "profiles" / name / "config.yaml").write_text("{}\n")

    def use(name: str) -> Path:
        home = root / "profiles" / name
        monkeypatch.setenv("HERMES_HOME", str(home))
        return home
    use.root = root
    return use


def _call(store, target, *operations):
    return json.loads(memory_tool(target=target, operations=list(operations), store=store))


def _store(**limits) -> MemoryStore:
    store = MemoryStore(**limits)
    store.load_from_disk()
    return store


def test_every_profile_resolves_one_shared_file(install):
    paths = {(install(name), get_shared_memory_dir())[1] for name in ("a", "b", "c")}
    assert paths == {install.root / "shared"}


@pytest.mark.parametrize("repeat, kind", [
    ("prefers  SHORT answers.", "exact"),
    ("Prefers shorter answers", "near"),
])
def test_per_agent_repeat_of_a_shared_fact_is_refused_naming_it(install, repeat, kind):
    install("a")
    assert _call(_store(), "shared", {"action": "add", "content": "Prefers short answers"})["success"]
    home = install("b")
    store = _store()
    refused = _call(store, "user", {"action": "add", "content": repeat})
    assert refused["success"] is False
    assert (refused["shared_entry"], refused["match"]) == ("Prefers short answers", kind)
    assert "'Prefers short answers'" in refused["error"]
    assert not (home / "memories" / "USER.md").exists() or "answers" not in (home / "memories" / "USER.md").read_text()
    assert _call(store, "user", {"action": "add", "content": "Works on the billing service"})["success"]


def test_patch_and_batch_cannot_write_a_shared_fact_into_the_agent_profile(install):
    install("a")
    assert _call(_store(), "shared", {"action": "add", "content": "Prefers short answers"})["success"]
    home = install("b")
    store = _store()
    assert _call(store, "user", {"action": "add", "content": "Prefers long answers"})["success"]
    patched = _call(store, "user", {"action": "patch", "old_text": "long", "content": "short"})
    assert patched["success"] is False and patched["shared_entry"] == "Prefers short answers"
    batch = _call(store, "user", {"action": "add", "content": "Has a cat"},
                  {"action": "add", "content": "Prefers short answers!"})
    assert batch["success"] is False and batch["shared_entry"] == "Prefers short answers"
    assert MemoryStore._read_file(home / "memories" / "USER.md") == ["Prefers long answers"]


def test_concurrent_processes_writing_the_shared_layer_keep_every_entry(install):
    """Four agent processes append to the shared layer at once; the lock + atomic rename keeps all."""
    writers, per_writer = ("a", "b", "c", "d"), 25
    (install.root / "profiles" / "d").mkdir()
    (install.root / "profiles" / "d" / "config.yaml").write_text("{}\n")
    script = (
        "import sys\n"
        "from tools.memory_tool import MemoryStore\n"
        "name, count = sys.argv[1], int(sys.argv[2])\n"
        "sys.stdin.read(1)\n"  # every writer starts on the same signal
        "store = MemoryStore(shared_user_char_limit=1_000_000)\n"
        "for i in range(count):\n"
        "    result = store.add('shared', f'{name} writes fact {i}')\n"
        "    assert result['success'], result\n")
    procs = []
    for name in writers:
        env = dict(os.environ, HERMES_HOME=str(install.root / "profiles" / name), PYTHONPATH=str(REPO))
        procs.append(subprocess.Popen([sys.executable, "-c", script, name, str(per_writer)], cwd=REPO, env=env,
                                      stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE))
    for proc in procs:
        proc.stdin.write(b"g")
        proc.stdin.close()
    for proc in procs:
        _out, err = proc.communicate(timeout=120)
        assert proc.returncode == 0, err.decode()
    entries = MemoryStore._read_file(install.root / "shared" / "OWNER.md")
    assert sorted(entries) == sorted(f"{name} writes fact {i}" for name in writers for i in range(per_writer))


def test_budgets_are_per_layer_and_a_full_rejection_names_its_layer(install):
    install("a")
    store = _store(user_char_limit=40, shared_user_char_limit=40)
    shared_ok = _call(store, "shared", {"action": "add", "content": "Owner lives by the sea"})
    user_ok = _call(store, "user", {"action": "add", "content": "Asked about the build"})
    assert (shared_ok["layer"], shared_ok["usage"]) == (MEMORY_LAYER_LABELS["shared"], "55% — 22/40 chars")
    assert (user_ok["layer"], user_ok["usage"]) == (MEMORY_LAYER_LABELS["user"], "52% — 21/40 chars")
    for target in ("shared", "user"):
        full = _call(store, target, {"action": "add", "content": "Owner prefers metric units always"})
        assert full["success"] is False and full["layer"] == MEMORY_LAYER_LABELS[target]
        assert f"The {MEMORY_LAYER_LABELS[target]} is full" in full["error"]
