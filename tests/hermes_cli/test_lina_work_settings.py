"""The work settings layer (lina#256) over a scratch LINA root: validation, revisions with an audit line each, one-step
rollback, an outside edit becoming its own revision, two writer processes, ``lina devices`` writing through the layer
and the route resolver. Every value is synthetic."""

from __future__ import annotations

import contextlib
import json
import os
import subprocess
import sys
import textwrap
from pathlib import Path

import pytest

import hermes_yaml

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
          planner: {provider: codex, id: plan-model, thinking: medium}
        defaults: {research: planner}
    """)


@pytest.fixture(autouse=True)
def _every_runner_keeps_its_type(tmp_path):
    yield
    from tests.hermes_cli._work_settings_scan import untyped_runners
    assert untyped_runners(tmp_path) == []


@pytest.fixture
def root(tmp_path, monkeypatch):
    base = tmp_path / "lina"
    base.mkdir()
    (base / "config.yaml").write_text(TODAY, encoding="utf-8")
    monkeypatch.setenv("LINA_HOME", str(base))
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


@pytest.mark.parametrize("entry", [
    {"role": "person"},
    {"role": "person", "host": ""},
    {"role": "person", "host": "   "},
    {"role": "person", "host": None},
])
def test_person_without_a_usable_host_is_refused_without_writing(root, entry):
    from hermes_cli.lina_work_settings import SettingsError
    from hermes_cli.worker import settings

    store = _store(root)
    store.current()
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(_set("devices.workers.laptop", entry), CLI)

    assert [error["path"] for error in refused.value.errors] == ["devices.workers.laptop.host"]
    assert (root / "config.yaml").read_bytes() == before
    assert [dev.name for dev in settings.devices()] == ["build-a", "here"]


@pytest.mark.parametrize(("key", "value"), [
    ("cap", 1), ("named_cap", 2), ("last", 0), ("auto", False),
    ("fast", True), ("no_repo", True), ("off", False), ("lina", "lina"),
])
def test_person_with_a_host_still_refuses_node_fields(root, key, value):
    from hermes_cli.lina_work_settings import SettingsError

    store = _store(root)
    store.current()
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(_set("devices.workers.laptop", {"host": "laptop-ssh", "role": "person", key: value}), CLI)

    assert [error["path"] for error in refused.value.errors] == [f"devices.workers.laptop.{key}"]
    assert (root / "config.yaml").read_bytes() == before


# ---------------------------------------------------------------- revisions, audit, comments
def test_a_change_is_one_revision_with_a_field_diff_and_keeps_the_comments_and_env(root):
    store = _store(root)
    start = store.current()
    assert start.id == "r1" and _audit(root)[0]["by"]["kind"] == "outside"
    rev, changes = store.apply(_set("worker.runners.codex.model", "codex/impl-model:high"), CLI)
    assert rev.id == "r2"
    assert changes == [{"path": "worker.runners.codex.model", "from": None, "to": "codex/impl-model:high"}]
    line = _audit(root)[-1]
    assert line["rev"] == "r2" and line["base"] == "r1" and line["by"] == CLI and line["changes"] == changes
    assert isinstance(line["at"], float)
    text = (root / "config.yaml").read_text(encoding="utf-8")
    assert "# the fast one" in text and "# the work devices" in text
    assert _config(root)["worker"]["runners"]["codex"]["env"] == {"CODEX_HOME": "/srv/codex"}
    assert _config(root)["worker"]["runners"]["codex"]["model"] == "codex/impl-model:high"
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
    store.apply(_set("worker.models.defaults.research", "codex/other-model"), CLI)
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
    monkeypatch.setenv("LINA_HOME", str(base))
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
    base, me, n = Path(sys.argv[1]), sys.argv[2], int(sys.argv[3])
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


def _switch(root):
    """A runner going from omo to command (type + argv) and a cap, in one change: three keys."""
    path = root / "config.yaml"
    path.write_text(TODAY.replace("  runners:\n", "  runners:\n    omo2: {type: omo}\n"), encoding="utf-8")
    store = _store(root)
    store.current()

    def edit(doc):
        doc["worker"]["runners"]["omo2"] = {"type": "command", "argv": ["omo2", "-"]}
        doc["devices"]["workers"]["build-a"]["cap"] = 3
        return doc
    return path, store, edit


def _whole(state: dict) -> bool:
    """The old settings or the new ones, never a mix."""
    runner, cap = state["worker"]["runners"]["omo2"], state["devices"]["workers"]["build-a"]["cap"]
    return (runner, cap) in (({"type": "omo"}, 2), ({"type": "command", "argv": ["omo2", "-"]}, 3))


def test_one_change_is_one_replacement_of_config_yaml_and_no_reader_sees_half_of_it(root, monkeypatch):
    import utils
    path, store, edit = _switch(root)
    real, seen = utils.atomic_replace, []

    def spy(*args, **kwargs):
        done = real(*args, **kwargs)
        if Path(args[1]) == path:  # a lina worker command reading config.yaml right after this replace
            seen.append(hermes_yaml.safe_load(path.read_text(encoding="utf-8")))
        return done
    monkeypatch.setattr(utils, "atomic_replace", spy)
    store.apply(edit, CLI)
    assert len(seen) == 1 and all(_whole(state) for state in seen)
    text = path.read_text(encoding="utf-8")
    assert "# the work devices" in text and "# the fast one" in text
    assert '      argv: [codex, exec, "-"]' in text  # an untouched line keeps its flow style and quotes
    assert text.index("devices:") < text.index("worker:") and text.index("min_free_gb") < text.index("runners:")


def test_a_write_cut_short_leaves_the_old_settings_or_the_new_ones_never_half(root, monkeypatch):
    import utils
    path, store, edit = _switch(root)
    real, calls = utils.atomic_replace, []

    def cut(*args, **kwargs):
        if Path(args[1]) == path:
            calls.append(1)
            if len(calls) == 2:  # the process dies before a second replacement of config.yaml
                raise KeyboardInterrupt
        return real(*args, **kwargs)
    monkeypatch.setattr(utils, "atomic_replace", cut)
    with contextlib.suppress(KeyboardInterrupt):
        store.apply(edit, CLI)
    assert _whole(_config(root))


@pytest.mark.parametrize("text", ["false\n", "[]\n", "just a line\n", "devices: [unclosed\n"],
                         ids=["false", "list", "scalar", "invalid"])
def test_a_config_that_cannot_be_read_as_a_mapping_is_never_overwritten(root, text):
    from hermes_cli.lina_work_settings import SettingsError
    path = root / "config.yaml"
    path.write_text(text, encoding="utf-8")
    with pytest.raises(SettingsError) as refused:
        _store(root).apply(_set("devices.workers.here", {"host": "local"}), CLI)
    assert "\n" not in str(refused.value) and str(refused.value).startswith("config.yaml: ")
    assert path.read_text(encoding="utf-8") == text
    assert list((root / "work-settings").glob("revisions/r*.yaml")) == []


def test_dotted_updates_honour_escaped_dots_and_existing_dotted_names():
    from hermes_cli.lina_work_settings import apply_updates
    doc = {"model_overrides": {"glm-5.3": {"x": 1}}, "keep": 1}
    out = apply_updates(doc, {"providers.qwen3\\.5.ctx": 8, "model_overrides.glm-5.3.x": 2, "keep": None})
    assert out == {"model_overrides": {"glm-5.3": {"x": 2}}, "providers": {"qwen3.5": {"ctx": 8}}}
    assert doc == {"model_overrides": {"glm-5.3": {"x": 1}}, "keep": 1}  # the input is not changed


SECRETS = {"tok-alias-1": "worker.models.aliases.planner.api_token", "tok-route-2": "worker.routing[0].secret_note",
           "tok-dev-3": "devices.workers.build-a.password", "tok-run-4": "worker.runners.codex.auth_token"}


def _with_secrets(root) -> Path:
    path = root / "config.yaml"
    text = TODAY.replace("cap: 2, fast: true}", "cap: 2, fast: true, password: tok-dev-3}")
    text = text.replace("thinking: medium}", "thinking: medium, api_token: tok-alias-1}")
    text = text.replace("      env: {CODEX_HOME: /srv/codex}\n", "      env: {CODEX_HOME: /srv/codex}\n      auth_token: tok-run-4\n")
    text += "  routing:\n    - {name: docs, when: {labels_any: [docs]}, runner: codex, secret_note: tok-route-2}\n"
    path.write_text(text, encoding="utf-8")
    return path


def test_credential_like_keys_at_any_depth_never_reach_a_revision_or_the_audit_and_stay_in_config(root):
    path = _with_secrets(root)
    store = _store(root)
    seen = store.current()
    stored = "".join(p.read_text(encoding="utf-8") for p in (root / "work-settings").rglob("*") if p.is_file())
    for value, where in SECRETS.items():
        leaf = where.rsplit(".", 1)[1]
        assert value not in stored and leaf not in stored, where
        assert value not in json.dumps(seen.doc)
    assert seen.doc["worker"]["routing"][0] == {"name": "docs", "when": {"labels_any": ["docs"]}, "runner": "codex"}
    store.apply(_set("devices.workers.build-a.cap", 3), CLI)
    text = path.read_text(encoding="utf-8")
    assert all(value in text for value in SECRETS) and _config(root)["devices"]["workers"]["build-a"]["cap"] == 3


FALSEY = [False, 0, "", []]
SECTIONS = [  # (patch putting the wrong value at the path, path); [] is valid for worker.routing (no rules)
    (lambda v: {"worker": v}, "worker"),
    (lambda v: {"worker": {"models": v}}, "worker.models"),
    (lambda v: {"worker": {"models": {"aliases": v}}}, "worker.models.aliases"),
    (lambda v: {"worker": {"models": {"defaults": v}}}, "worker.models.defaults"),
    (lambda v: {"worker": {"models": {"fallback": v}}}, "worker.models.fallback"),
    (lambda v: {"worker": {"models": {"aliases": {"planner": v}}}}, "worker.models.aliases.planner"),
    (lambda v: {"worker": {"runners": v}}, "worker.runners"),
    (lambda v: {"worker": {"runners": {"codex": v}}}, "worker.runners.codex"),
    (lambda v: {"worker": {"routing": v}}, "worker.routing"),
    (lambda v: {"worker": {"routing": [v]}}, "worker.routing[0]"),
    (lambda v: {"worker": {"routing": [{"name": "r", "runner": "omo", "when": v}]}}, "worker.routing[0].when"),
    (lambda v: {"devices": v}, "devices"),
    (lambda v: {"devices": {"workers": v}}, "devices.workers"),
    (lambda v: {"devices": {"workers": {"build-a": v}}}, "devices.workers.build-a"),
]


CASES = [(patch, where, value) for patch, where in SECTIONS for value in FALSEY
         if not (where == "worker.routing" and value == [])]


@pytest.mark.parametrize(("patch", "where", "value"), CASES, ids=[f"{w}={v!r}" for _, w, v in CASES])
def test_a_falsey_wrong_type_on_any_section_is_refused_with_its_path(root, patch, where, value):
    from hermes_cli.lina_work_settings import SettingsError, merge_patch
    store = _store(root)
    store.current()
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(lambda doc: merge_patch(doc, patch(value)), CLI)
    assert where in [e["path"] for e in refused.value.errors], refused.value.errors
    assert (root / "config.yaml").read_bytes() == before
    assert [line["rev"] for line in _audit(root)] == ["r1"]


def test_a_mapping_holding_an_excluded_field_is_never_replaced_by_a_non_mapping(root):
    from hermes_cli.lina_work_settings import SettingsError, merge_patch, remnants, snapshot
    path = root / "config.yaml"
    path.write_text(TODAY.replace("    defaults: {research: planner}\n",
                                  "    defaults: {research: planner}\n    api_token: tok-models-1\n"), encoding="utf-8")
    store = _store(root)
    store.current()
    with pytest.raises(SettingsError) as refused:
        store.apply(lambda doc: merge_patch(doc, {"worker": {"models": False}}), CLI)
    assert "worker.models" in [e["path"] for e in refused.value.errors]
    assert "tok-models-1" in path.read_text(encoding="utf-8")
    raw = _config(root)
    old = snapshot(raw)
    for value in FALSEY:  # the write guard on its own, whatever validation says
        new = merge_patch(old, {"worker": {"models": value}})
        assert "worker.models" in [e["path"] for e in remnants(old, new, raw)], value


REMNANT = "worker.runners.codex: has fields the work settings don't manage (env)"


@pytest.mark.parametrize("path", ["worker.runners.codex", "worker.runners"])
def test_removing_a_runner_that_holds_env_is_refused_and_writes_nothing(root, path):
    from hermes_cli.lina_work_settings import SettingsError
    store = _store(root)
    store.current()
    before = (root / "config.yaml").read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.apply(_set(path, None), CLI)
    assert str(refused.value).startswith(REMNANT) and "on: false" in str(refused.value)
    assert (root / "config.yaml").read_bytes() == before
    assert [line["rev"] for line in _audit(root)] == ["r1"]


def test_turning_a_runner_off_keeps_its_env_type_and_argv(root):
    store = _store(root)
    store.current()
    store.apply(_set("worker.runners.codex.on", False), CLI)
    assert _config(root)["worker"]["runners"]["codex"] == {
        "type": "command", "argv": ["codex", "exec", "-"], "env": {"CODEX_HOME": "/srv/codex"}, "on": False}


def test_a_rollback_that_would_leave_a_runner_without_its_type_is_refused(root):
    from hermes_cli.lina_work_settings import SettingsError
    path = root / "config.yaml"
    path.write_text(TODAY.replace("    codex:\n      type: command\n      argv: [codex, exec, \"-\"]\n"
                                  "      env: {CODEX_HOME: /srv/codex}\n", "    dsh: {type: command, argv: [dsh, \"-\"]}\n"),
                    encoding="utf-8")
    store = _store(root)
    store.current()
    store.apply(_set("worker.runners.codex", {"type": "command", "argv": ["codex", "-"]}), CLI)
    path.write_text(path.read_text(encoding="utf-8").replace("codex:\n", "codex:\n      env: {CODEX_HOME: /srv/codex}\n"),
                    encoding="utf-8")  # env added by hand: not a work setting, so no new revision
    before = path.read_bytes()
    with pytest.raises(SettingsError) as refused:
        store.rollback({**CLI, "kind": "rollback"})
    assert str(refused.value).startswith(REMNANT)
    assert path.read_bytes() == before
    assert [line["rev"] for line in store.history()] == ["r2", "r1"]


def _typed_doc() -> dict:
    return {"devices": {"workers": {"here": {"host": "local"}}},
            "worker": {"default_runner": "omo",
                       "runners": {"codex": {"type": "command", "argv": ["codex", "-"]}},
                       "models": {"aliases": {"planner": {"provider": "p", "id": "i"}},
                                  "defaults": {"research": "planner"}, "fallback": "p/m"},
                       "routing": [{"name": "r", "when": {"labels_any": ["a"]}, "runner": "codex"}]}}


FIELDS = [*(f"devices.workers.here.{k}" for k in ("host", "role", "cap", "named_cap", "last", "fast", "no_repo",
                                                   "off", "auto", "lina")),
          *(f"worker.runners.codex.{k}" for k in ("type", "argv", "follow_up_argv", "model_args", "on", "model",
                                                   "fallback_model", "access", "bin", "node", "engine")),
          *(f"worker.models.aliases.planner.{k}" for k in ("provider", "id", "thinking")),
          "worker.models.defaults.research", "worker.models.fallback", "worker.default_runner",
          "worker.routing[0].name", "worker.routing[0].runner", "worker.routing[0].when.runner_label",
          "worker.routing[0].when.labels_any", "worker.routing[0].when.size"]
WRONG = [[["x"]], {"k": ["v"]}]


def _put(doc: dict, field: str, value) -> dict:
    from hermes_cli.lina_work_settings import set_path
    if field.startswith("worker.routing[0]."):
        rule = doc["worker"]["routing"][0]
        set_path(rule, field.removeprefix("worker.routing[0]."), value)
        return doc
    return set_path(doc, field, value)


@pytest.mark.parametrize("value", WRONG, ids=["nested-list", "mapping"])
@pytest.mark.parametrize("field", FIELDS)
def test_every_wrong_type_on_every_field_is_refused_with_its_path_never_an_exception(field, value):
    from hermes_cli.lina_work_settings import validate
    assert validate(_typed_doc()) == []
    errors = validate(_put(_typed_doc(), field, value))
    assert any(e["path"] == field or e["path"].startswith((field + ".", field + "[")) for e in errors), errors


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


@pytest.mark.parametrize("routing", [5, "docs", {"name": "x"}, [{"name": "r", "runner": "agy", "when": "x"}],
                                     [{"name": "r", "runner": "agy", "when": {"labels_any": 3, "size": 7}}]])
def test_the_route_resolver_reads_a_wrong_typed_routing_as_no_rule_and_never_crashes(routing):
    from hermes_cli.lina_work_settings_route import route
    doc = _routed_doc()
    doc["worker"]["routing"] = routing
    assert route(doc, ["docs"], "S")["runner"] in ("omo", "agy")


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
