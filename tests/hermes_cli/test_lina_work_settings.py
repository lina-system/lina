"""The work settings layer (lina#256) over a scratch LINA root: validation, revisions with an audit line each, one-step
rollback, an outside edit becoming its own revision, two writer processes, ``lina devices`` writing through the layer
and the route resolver. Every value is synthetic."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

import hermes_yaml
from hermes_cli import lina_ocx

pytestmark = pytest.mark.platforms("posix")

REPO = Path(__file__).resolve().parents[2]
CLI = {"kind": "cli", "principal": "tester", "device": "local"}
TODAY = textwrap.dedent("""\
    # the work devices
    devices:
      workers:
        build-a: {host: build-a, cap: 2, fast: true}   # the fast one
        here: {host: local, cap: 1, named_cap: 2, last: 2}
    worker:
      min_free_gb: 5
      default_runner: omo
      runners:
        codex:
          type: command
          argv: [codex, exec, "-"]
          env: {CODEX_HOME: /srv/codex}
      models:
        aliases:
          planner: {provider: opencodex, id: plan-model, thinking: medium}
        defaults: {research: planner}
    """)


@pytest.fixture
def root(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(TODAY, encoding="utf-8")
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: base)
    return base


def _store(base):
    from hermes_cli.lina_work_settings_store import Store
    return Store(base)


def _config(base) -> dict:
    return hermes_yaml.safe_load((base / "config.yaml").read_text(encoding="utf-8"))


def _audit(base) -> list[dict]:
    path = base / "work-settings" / "audit.jsonl"
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()] if path.is_file() else []


def _set(path: str, value):
    from hermes_cli.lina_work_settings import set_path

    def edit(doc):
        set_path(doc, path, value)
        return doc
    return edit


# ---------------------------------------------------------------- validation
@pytest.mark.parametrize(("path", "value", "where"), [
    ("worker.runners.codex.colour", "blue", "worker.runners.codex.colour"),
    ("worker.default_runner", "nobody", "worker.default_runner"),
    ("worker.routing", [{"name": "docs", "when": {"labels_any": ["docs"]}, "runner": "ghost"}], "worker.routing[0].runner"),
    ("devices.workers.laptop", {"role": "person", "cap": 1}, "devices.workers.laptop.cap"),
    ("devices.workers.laptop", {"role": "person", "named_cap": 2}, "devices.workers.laptop.named_cap"),
    ("worker.runners.codex.api_key", "abc", "worker.runners.codex.api_key"),
    ("devices.workers.build-a.token", "abc", "devices.workers.build-a.token"),
    ("worker.runners.codex.env", {"A": "b"}, "worker.runners.codex.env"),
    ("worker.runners.codex.model", "no-slash-and-no-alias", "worker.runners.codex.model"),
    ("devices.workers.build-a.cap", 0, "devices.workers.build-a.cap"),
    ("worker.colour", "blue", "worker.colour"),
])
def test_a_refused_change_names_the_path_and_writes_nothing(root, path, value, where):
    from hermes_cli.lina_work_settings import SettingsError
    store = _store(root)
    store.current()
    before = (root / "config.yaml").read_bytes()
    revisions = sorted((root / "work-settings" / "revisions").iterdir())
    with pytest.raises(SettingsError) as refused:
        store.apply(_set(path, value), CLI)
    assert where in [error["path"] for error in refused.value.errors]
    assert (root / "config.yaml").read_bytes() == before
    assert sorted((root / "work-settings" / "revisions").iterdir()) == revisions


def test_today_s_config_validates_and_its_env_never_reaches_a_revision(root):
    from hermes_cli.lina_work_settings import validate
    current = _store(root).current()
    assert validate(current.doc) == []
    assert "env" not in current.doc["worker"]["runners"]["codex"]
    assert "min_free_gb" not in current.doc["worker"]  # not a work setting: never in a revision
    text = next((root / "work-settings" / "revisions").iterdir()).read_text(encoding="utf-8")
    assert "/srv/codex" not in text


# ---------------------------------------------------------------- revisions, audit, comments
def test_a_change_is_one_revision_with_a_field_diff_and_keeps_the_comments_and_env(root):
    store = _store(root)
    start = store.current()
    assert start.id == "r1" and _audit(root)[0]["by"]["kind"] == "outside"
    rev, changes = store.apply(_set("worker.runners.codex.model", "opencodex/impl-model:high"), CLI)
    assert rev.id == "r2"
    assert changes == [{"path": "worker.runners.codex.model", "from": None, "to": "opencodex/impl-model:high"}]
    line = _audit(root)[-1]
    assert line["rev"] == "r2" and line["base"] == "r1" and line["by"] == CLI and line["changes"] == changes
    assert isinstance(line["at"], float)
    text = (root / "config.yaml").read_text(encoding="utf-8")
    assert "# the fast one" in text and "# the work devices" in text
    assert _config(root)["worker"]["runners"]["codex"]["env"] == {"CODEX_HOME": "/srv/codex"}
    assert _config(root)["worker"]["runners"]["codex"]["model"] == "opencodex/impl-model:high"
    assert (root / "work-settings" / "revisions" / "r000002.yaml").is_file()


def test_a_stale_base_is_refused(root):
    from hermes_cli.lina_work_settings_store import Stale
    store = _store(root)
    first = store.current()
    store.apply(_set("devices.workers.build-a.cap", 3), CLI)
    with pytest.raises(Stale):
        store.apply(_set("devices.workers.build-a.cap", 4), CLI, expect=first.etag)
    assert _config(root)["devices"]["workers"]["build-a"]["cap"] == 3


def test_two_rollbacks_return_to_the_start(root):
    store = _store(root)
    store.current()
    store.apply(_set("devices.workers.build-a.cap", 3), CLI)
    start = store.current()
    store.apply(_set("worker.models.defaults.research", "opencodex/other-model"), CLI)
    changed = _config(root)
    once, _ = store.rollback({**CLI, "kind": "rollback"})
    assert once.doc == store.revision(start.n).doc and _config(root)["worker"]["models"]["defaults"] == {
        "research": "planner"}
    twice, _ = store.rollback({**CLI, "kind": "rollback"})
    assert twice.doc != start.doc and _config(root)["worker"]["models"] == changed["worker"]["models"]
    third, _ = store.rollback({**CLI, "kind": "rollback"})
    assert third.doc == start.doc
    kinds = [line["by"]["kind"] for line in _audit(root)]
    assert kinds[-3:] == ["rollback"] * 3 and _audit(root)[-1]["rollback_of"] == twice.id


def test_an_outside_edit_is_caught_on_the_next_read_as_its_own_revision(root):
    store = _store(root)
    store.current()
    path = root / "config.yaml"
    path.write_text(path.read_text(encoding="utf-8").replace("cap: 2, fast: true", "cap: 5, fast: true"),
                    encoding="utf-8")
    seen = store.current()
    assert seen.id == "r2" and seen.doc["devices"]["workers"]["build-a"]["cap"] == 5
    line = _audit(root)[-1]
    assert line["by"]["kind"] == "outside" and line["changes"] == [
        {"path": "devices.workers.build-a.cap", "from": 2, "to": 5}]
    assert store.current().id == "r2"  # nothing changed since: no new revision


def test_a_fresh_root_starts_empty_and_the_first_change_is_r1(tmp_path, monkeypatch):
    base = tmp_path / "fresh"
    base.mkdir()
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: base)
    store = _store(base)
    assert store.current().id == "r0" and store.current().doc == {}
    rev, _ = store.apply(_set("devices.workers.here", {"host": "local", "cap": 1}), CLI)
    assert rev.id == "r1" and _config(base)["devices"]["workers"]["here"] == {"host": "local", "cap": 1}
    back, _ = store.rollback({**CLI, "kind": "rollback"})
    assert back.id == "r2" and back.doc == {} and "here" not in ((_config(base) or {}).get("devices") or {}).get(
        "workers", {})


WRITER = textwrap.dedent("""\
    import sys
    from pathlib import Path
    from hermes_cli import lina_ocx
    base, me, n = Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
    lina_ocx.lina_root = lambda: base
    from hermes_cli.lina_work_settings import set_path
    from hermes_cli.lina_work_settings_store import Store
    go = base / "go"
    while not go.exists():
        pass
    store = Store(base)
    for i in range(1, n + 1):
        store.apply(lambda doc: set_path(doc, f"devices.workers.{me}.cap", i) or doc,
                    {"kind": "cli", "principal": me, "device": "local"})
    """)


def test_two_writer_processes_never_make_the_same_revision(root):
    path = root / "config.yaml"
    path.write_text(path.read_text(encoding="utf-8").replace(
        "    here:", "    w0: {host: w0}\n    w1: {host: w1}\n    here:"), encoding="utf-8")
    _store(root).current()
    env = {**os.environ, "PYTHONPATH": str(REPO)}
    procs = [subprocess.Popen([sys.executable, "-c", WRITER, str(root), f"w{k}", "12"], cwd=REPO, env=env,
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
             for k in (0, 1)]
    (root / "go").write_text("", encoding="utf-8")
    for proc in procs:
        _, err = proc.communicate(timeout=120)
        assert proc.returncode == 0, err.decode()
    lines = _audit(root)
    revs = [line["rev"] for line in lines]
    assert revs == [f"r{n}" for n in range(1, 26)]  # r1 = the start, then 24 changes, each its own number
    assert [line["base"] for line in lines[1:]] == revs[:-1]
    files = sorted(p.name for p in (root / "work-settings" / "revisions").iterdir())
    assert files == [f"r{n:06d}.yaml" for n in range(1, 26)]
    workers = _config(root)["devices"]["workers"]
    assert workers["w0"]["cap"] == 12 and workers["w1"]["cap"] == 12


SHARED = textwrap.dedent("""\
    devices:
      probe_s: 30   # not a work setting
    worker:
      hub: {host: hub, lina: lina}   # how a device reaches the hub
      min_free_gb: 7
      settings:
        owner: {login: owner@example.test, devices: [phone-a]}
    """)
KEPT = ["  probe_s: 30   # not a work setting", "  hub: {host: hub, lina: lina}   # how a device reaches the hub",
        "  min_free_gb: 7", "  settings:", "    owner: {login: owner@example.test, devices: [phone-a]}"]


def test_writes_touch_only_work_settings_keys_and_keep_the_rest_of_shared_sections(root):
    (root / "config.yaml").write_text(SHARED, encoding="utf-8")
    store = _store(root)
    assert store.current().id == "r0"

    def kept() -> None:
        lines = (root / "config.yaml").read_text(encoding="utf-8").splitlines()
        assert all(line in lines for line in KEPT), lines

    store.apply(_set("worker.default_runner", "omo"), CLI)
    store.apply(_set("devices.workers.here", {"host": "local"}), CLI)
    kept()
    assert _config(root)["worker"]["default_runner"] == "omo"
    store.apply(_set("devices.workers", None), CLI)
    store.apply(_set("worker.default_runner", None), CLI)  # the last work setting goes, the sections stay
    kept()
    assert "default_runner" not in _config(root)["worker"] and "workers" not in _config(root)["devices"]
    store.rollback({**CLI, "kind": "rollback"})
    kept()
    assert _config(root)["worker"]["default_runner"] == "omo"
    store.rollback({**CLI, "kind": "rollback"})
    kept()
    assert store.current().doc == {} and "default_runner" not in _config(root)["worker"]


@pytest.mark.parametrize(("patch", "where"), [
    ({"worker": "broken"}, "worker"),
    ({"devices": []}, "devices"),
    ({"devices": {"workers": []}}, "devices.workers"),
    ({"worker": {"runners": "codex"}}, "worker.runners"),
    ({"worker": {"runners": {"codex": "command"}}}, "worker.runners.codex"),
    ({"worker": {"models": ["planner"]}}, "worker.models"),
    ({"devices": {"workers": {"build-a": 2}}}, "devices.workers.build-a"),
])
def test_a_section_or_entry_that_is_not_a_mapping_is_refused_with_its_path(root, patch, where):
    from hermes_cli.lina_work_settings import SettingsError, merge_patch
    store = _store(root)
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(lambda doc: merge_patch(doc, patch), CLI)
    assert where in [error["path"] for error in refused.value.errors]
    assert (root / "config.yaml").read_bytes() == before


# ---------------------------------------------------------------- lina devices through the layer
def test_lina_devices_add_and_remove_show_in_the_history(root):
    from hermes_cli import lina_devices
    lina_devices.add("build-z", "build-z", base=root, cap=2, out=lambda _line: None)
    assert _config(root)["devices"]["workers"]["build-z"] == {"host": "build-z", "cap": 2}
    lina_devices.remove("build-z", base=root, out=lambda _line: None)
    assert "build-z" not in _config(root)["devices"]["workers"]
    tail = _audit(root)[-2:]
    assert [line["by"]["kind"] for line in tail] == ["cli", "cli"]
    assert tail[0]["changes"][0]["path"].startswith("devices.workers.build-z")
    assert tail[1]["changes"][0]["path"].startswith("devices.workers.build-z")


# ---------------------------------------------------------------- route resolver
ROUTING = [{"name": "docs", "when": {"labels_any": ["docs", "writing"]}, "runner": "agy"},
           {"name": "small-code", "when": {"runner_label": "runner/dsh", "size": ["S"]}, "runner": "dsh"},
           {"name": "medium", "when": {"size": ["M"]}, "runner": "codex"}]


def _routed_doc() -> dict:
    return {"worker": {"default_runner": "omo", "routing": ROUTING,
                       "runners": {"agy": {"type": "command", "argv": ["agy", "-"]},
                                   "dsh": {"type": "command", "argv": ["dsh", "-"]},
                                   "codex": {"type": "command", "argv": ["codex", "-"], "on": False}}}}


@pytest.mark.parametrize(("labels", "size", "runner", "rule"), [
    (["writing"], "L", "agy", "docs"),
    (["runner/dsh", "docs"], "S", "agy", "docs"),  # the first match wins
    (["runner/dsh"], "S", "dsh", "small-code"),
    (["runner/dsh"], "M", "omo", None),  # medium's runner is off: skipped, then the default
    ([], None, "omo", None),
])
def test_the_route_resolver_takes_the_first_matching_rule_else_the_default(labels, size, runner, rule):
    from hermes_cli.lina_work_settings import validate
    from hermes_cli.lina_work_settings_route import route
    doc = _routed_doc()
    assert validate(doc) == []
    found = route(doc, labels, size)
    assert (found["runner"], found["rule"]) == (runner, rule)
