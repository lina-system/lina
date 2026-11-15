"""`lina heart import` and `lina migrate`'s heart step: a trial agent's files and journal on a LINA home (lina#356)."""

import json
from pathlib import Path

import pytest

from tests.hermes_cli.test_heart_journal import database, invoke, sandbox

__all__ = ["sandbox"]

MAPPED, UNMAPPED = 162710, 162711
FILES = {
    "SOUL.self.md": "Synthetic self note (2026-11-10, evidence 2026-11-10T23:40:00+09:00)\n",
    "heart/preferences.json": json.dumps({"version": 1, "agent": "alpha", "created": "2026-11-01", "updated":
                                          "2026-11-10", "journal_seen": 3, "next_id": 2, "records": [],
                                          "rejected": [], "tombstones": [], "expired": []}),
    "heart/changes.jsonl": json.dumps({"ts": "2026-11-10T23:50:00+09:00", "removed": [], "added": ["Synthetic"],
                                       "evidence": ["2026-11-10T23:40:00+09:00"], "via": "review"}) + "\n",
    "heart/temperament-seed.json": json.dumps({"seed": "synthetic"}),
    "heart/quiz/day0.json": json.dumps({"answers": []}),
    "heart/quiz/notes.md": "# Synthetic quiz\n",
    "heart/quiz/raw.txt": "synthetic\n",
    "heart/self-backups/2026-11-09T230000+0900.md": "Older synthetic self note\n",
}


def line(event, store_ids, **extra):
    return {"ts": "2026-11-10T23:40:00+09:00", "event": event, "feeling": "calm", "note": "Synthetic note",
            "evidence": [{"store_id": store_id, "session_id": "20261110_145224_d066203a"} for store_id in store_ids],
            "strength": 1, "via": "review", **extra}


JOURNAL = [line("Mapped", [MAPPED]), line("Half mapped", [MAPPED, UNMAPPED]),
           line("With taste", [UNMAPPED], pref={"target": "long bullet lists", "kind": "tone", "dir": "dislike",
                                                 "route": "reaction"})]


def trial_home(root: Path, journal=JOURNAL, extra_lines=()) -> Path:
    for relative, content in FILES.items():
        (root / relative).parent.mkdir(parents=True, exist_ok=True)
        (root / relative).write_text(content)
    (root / "heart/journal.jsonl").write_text(
        "".join(json.dumps(entry) + "\n" for entry in journal) + "".join(raw + "\n" for raw in extra_lines))
    return root


@pytest.fixture
def moved_lcm(sandbox, memory_store_sql):
    """The agent's own turn and the LCM row `lina migrate` moved for it, keeping the trial's store_id."""
    owner = database(sandbox["alpha"])[0]
    memory_store_sql("INSERT INTO ctx.lcm_messages (agent_id, store_id, session_id, text_uid, content_sha256,"
                     " timestamp) VALUES ('alpha', %s, 'owner', %s, '', 0)", (MAPPED, owner))
    return owner


def heart_rows(memory_store_sql, agent="alpha"):
    return memory_store_sql("SELECT uid, scope, content, native FROM memory.messages WHERE source = 'heart'"
                            " AND agent_id = %s ORDER BY message_id", (agent,))


def test_import_carries_files_privately_and_journal_as_rows(sandbox, tmp_path, moved_lcm, memory_store_sql):
    # Given a trial agent's home with every heart file and a three-line journal
    source = trial_home(tmp_path / "trial")
    home = sandbox["alpha"]
    # When
    code, output, error = invoke("import", "--from", str(source))
    # Then every file is here unchanged and private
    assert code == 0, error
    for relative, content in FILES.items():
        assert (home / relative).read_text() == content
        assert (home / relative).stat().st_mode & 0o777 == 0o600
    for directory in ("heart", "heart/quiz", "heart/self-backups"):
        assert (home / directory).stat().st_mode & 0o777 == 0o700
    assert not (home / "heart/journal.jsonl").exists()
    # and each journal line is one row: 3 in, 3 out, the store_id with no moved row listed and kept
    found = heart_rows(memory_store_sql)
    assert len(found) == 3 and {scope for _, scope, _, _ in found} == {"agent"}
    assert [native["evidence"] for _, _, _, native in found] == [[moved_lcm], [moved_lcm], []]
    assert [native.get("unmapped_evidence") for _, _, _, native in found][1:] == [
        [{"store_id": UNMAPPED, "session_id": "20261110_145224_d066203a"}]] * 2
    assert found[2][3]["pref"] == {"target": "long bullet lists", "kind": "tone", "dir": "dislike", "route": "reaction"}
    assert "Journal: 3 lines read, 3 rows written, 0 already present, 0 unreadable" in output
    assert f"line 2: evidence not in the moved LCM rows {UNMAPPED}" in output
    assert f"line 3: evidence not in the moved LCM rows {UNMAPPED}" in output
    listed = json.loads(invoke("list", "--json")[1])
    assert [entry["event"] for entry in listed] == ["Mapped", "Half mapped", "With taste"]
    assert listed[0]["ts"] == "2026-11-10T23:40:00+09:00"


def test_a_journal_alone_still_creates_the_private_heart_directory(sandbox, tmp_path, moved_lcm, memory_store_sql):
    # Given a source with a journal and none of the optional heart files
    source = tmp_path / "trial"
    (source / "heart").mkdir(parents=True)
    (source / "heart/journal.jsonl").write_text(json.dumps(JOURNAL[0]) + "\n")
    home = sandbox["alpha"]
    assert not (home / "heart").exists()
    # When
    code, _, error = invoke("import", "--from", str(source))
    # Then the journal rows are here and the heart directory marking a journal-keeping agent is private
    assert code == 0, error
    assert (home / "heart").stat().st_mode & 0o777 == 0o700
    assert len(heart_rows(memory_store_sql)) == 1


def test_second_import_adds_nothing(sandbox, tmp_path, moved_lcm, memory_store_sql):
    source = trial_home(tmp_path / "trial")
    assert invoke("import", "--from", str(source))[0] == 0
    before = heart_rows(memory_store_sql)
    code, output, _ = invoke("import", "--from", str(source))
    assert code == 0
    assert heart_rows(memory_store_sql) == before
    assert "Files: copied 0, unchanged 8, skipped 0" in output
    assert "Journal: 3 lines read, 0 rows written, 3 already present" in output


def test_dry_run_writes_nothing_and_counts(sandbox, tmp_path, moved_lcm, memory_store_sql):
    source = trial_home(tmp_path / "trial")
    code, output, _ = invoke("import", "--from", str(source), "--dry-run")
    assert code == 0
    assert "Files: copied 8, unchanged 0, skipped 0" in output
    assert "Journal: 3 lines read, 3 rows written, 0 already present" in output
    assert heart_rows(memory_store_sql) == []
    assert not (sandbox["alpha"] / "SOUL.self.md").exists() and not (sandbox["alpha"] / "heart").exists()


def test_a_differing_file_here_is_skipped_and_listed(sandbox, tmp_path, moved_lcm):
    source = trial_home(tmp_path / "trial")
    (sandbox["alpha"] / "SOUL.self.md").write_text("Newer self note written here\n")
    code, output, _ = invoke("import", "--from", str(source))
    assert code == 0
    assert (sandbox["alpha"] / "SOUL.self.md").read_text() == "Newer self note written here\n"
    assert "skipped SOUL.self.md: the file here is newer" in output


def test_unreadable_line_is_listed_and_the_rest_kept(sandbox, tmp_path, moved_lcm, memory_store_sql):
    source = trial_home(tmp_path / "trial", extra_lines=["{not json", json.dumps({"event": "no other fields"})])
    code, output, _ = invoke("import", "--from", str(source))
    assert code == 1
    assert len(heart_rows(memory_store_sql)) == 3
    assert "Journal: 5 lines read, 3 rows written, 0 already present, 2 unreadable" in output
    assert "line 4 unreadable" in output and "line 5 unreadable" in output


def test_lina_migrate_carries_the_heart_after_the_lcm_rows(tmp_path, _memory_store, memory_store_sql):
    from tests.hermes_cli.test_migrate_memory import _command, _run

    command = _command(tmp_path, _memory_store)
    # lcm_fixture moves store_ids 1-3; 99 has no moved row
    trial_home(tmp_path / "hermes/profiles/alpha", journal=[line("Moved", [1, 99])])
    done = _run(command, tmp_path)
    assert done.returncode == 0, done.stdout + done.stderr
    report = json.loads(done.stdout)
    assert report["complete"] and report["steps"]["heart"] == {"counted": True, "ok": True}
    assert report["heart"]["unmapped"] == [[1, ["99"]]] and report["heart"]["written"] == 1
    destination = tmp_path / "lina/profiles/alpha-copy"
    assert (destination / "SOUL.self.md").stat().st_mode & 0o777 == 0o600
    assert (destination / "heart/preferences.json").read_text() == FILES["heart/preferences.json"]
    assert not (destination / "heart/journal.jsonl").exists()
    [(uid, _, _, native)] = heart_rows(memory_store_sql, "alpha-copy")
    [(text_uid,)] = memory_store_sql("SELECT text_uid FROM ctx.lcm_messages WHERE agent_id = 'alpha-copy'"
                                     " AND store_id = 1")
    assert uid.startswith("heart:alpha-copy:") and native["evidence"] == [text_uid]
    again = _run(command, tmp_path)
    assert again.returncode == 0, again.stdout + again.stderr
    assert json.loads(again.stdout)["heart"]["written"] == 0
    assert len(heart_rows(memory_store_sql, "alpha-copy")) == 1
