"""Curator/work adapters execute only their owning CLI and consume receipts."""

import json
import subprocess

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


def test_skill_rollback_profile_receipt_and_inverse(undo, monkeypatch):
    import hashlib
    # Given a real synthetic skill ledger and edited skill.
    home = undo.profiles["alpha"]
    skill = home / "skills/example.md"
    old, new = b"original\r\n\xff", b"edited\n"
    skill.write_bytes(new)
    ledger = home / "skills/.curator_ledger.jsonl"
    first = {"id": "entry-1", "action": "patch",
             "before": [{"path": str(skill), "sha256": hashlib.sha256(old).hexdigest()}],
             "after": [{"path": str(skill), "sha256": hashlib.sha256(new).hexdigest()}]}
    ledger.write_text(json.dumps(first) + "\n")
    calls = []

    def run(argv, **kwargs):
        calls.append((argv, kwargs))
        rows = [json.loads(line) for line in ledger.read_text().splitlines()]
        target = next(r for r in rows if r["id"] == argv[-2])
        receipt = {"id": f"rollback-{len(rows)}", "action": "rollback", "before": target["after"],
                   "after": target["before"], "evidence": {"rollback_target": target["id"]}}
        skill.write_bytes(old if len(rows) == 1 else new)
        with ledger.open("a") as stream:
            stream.write(json.dumps(receipt) + "\n")
        return subprocess.CompletedProcess(argv, 0)

    monkeypatch.setattr(subprocess, "run", run)
    assert undo.log([{"type": "skill", "profile": "alpha", "entry_id": "entry-1"}])[0] == 0
    # When applying and then applying the receipt's inverse.
    undo.ok("apply", "change-1")
    restored = skill.read_bytes()
    inverse = undo.journal()[-1]
    undo.ok("apply", inverse["change_id"])
    # Then profile binding, suppression and both directions are preserved.
    assert restored == old and skill.read_bytes() == new
    assert calls[0][0][-4:] == ["curator", "rollback", "entry-1", "--yes"]
    assert calls[0][1]["env"]["HERMES_HOME"] == str(home)
    assert calls[0][1]["stdout"] == subprocess.DEVNULL
    assert calls[0][1]["stderr"] == subprocess.DEVNULL
    assert inverse["objects"][0]["entry_id"] == "rollback-1"


def test_skill_later_bytes_refused_before_command(undo, monkeypatch):
    import hashlib
    # Given a ledger after-state changed by another writer.
    home = undo.profiles["alpha"]
    skill = home / "skills/example.md"
    skill.write_bytes(b"later writer")
    row = {"id": "entry-1", "action": "patch", "before": [],
           "after": [{"path": str(skill), "sha256": hashlib.sha256(b"recorded").hexdigest()}]}
    (home / "skills/.curator_ledger.jsonl").write_text(json.dumps(row) + "\n")
    assert undo.log([{"type": "skill", "profile": "alpha", "entry_id": "entry-1"}])[0] == 0
    calls = []
    monkeypatch.setattr(subprocess, "run", lambda *a, **k: calls.append(a))
    # When applying it.
    code, _, _ = undo.run("apply", "change-1")
    # Then no curator command or inverse is produced.
    assert code == 2 and calls == [] and len(undo.journal()) == 1


def test_work_undo_expected_revision_and_inverse(undo, monkeypatch):
    from hermes_cli.work import env, ledger
    from tests.hermes_cli._work_env import WorkEnv
    # Given an actual SQLite work item with two revisions.
    with monkeypatch.context() as setup:
        setup.setattr(undo, "run", WorkEnv.run.__get__(undo))
        undo.fixture()
    env.reset()
    rows = [r for r in ledger.rows("T1") if not r.get("_meta")]
    first = ledger.append(dict(rows[-1], rev=0), "edit", "alpha")
    second = ledger.append(first, "edit", "alpha")
    before = int(second["rev"])
    ledger.close()
    calls = []

    def run(argv, **kwargs):
        calls.append(argv)
        env.bind(env.load())
        history = [r for r in ledger.rows("T1") if not r.get("_meta")]
        revision = int(history[-1]["rev"])
        assert revision == int(argv[argv.index("--expect") + 1])
        target = next(r for r in history if r.get("rev") == int(argv[argv.index("--to") + 1]))
        ledger.append(dict(target, rev=revision), "undo", "alpha")
        ledger.close()
        return subprocess.CompletedProcess(argv, 0)

    monkeypatch.setattr(subprocess, "run", run)
    assert undo.log([{"type": "ledger", "id": "T1", "rev": 1, "after": before}])[0] == 0
    # When applying it through the subprocess boundary.
    from hermes_cli.undo.apply import apply
    from hermes_cli.undo.env import load
    from hermes_cli.undo.storage import Store
    store = Store()
    apply(store, load(store), "change-1")
    # Then work owns the revision append and undo records its exact inverse.
    inverse = undo.journal()[-1]
    assert inverse["objects"] == [{"type": "ledger", "id": "T1", "rev": before, "after": before + 1}]
    assert "--expect" in calls[0] and "--as" not in calls[0]
