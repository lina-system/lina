"""Weekly counts, privacy, config, locales and profile resolution."""

import json

import pytest

from hermes_cli.heart import storage
from hermes_cli.heart.models import Change, Entry
from hermes_cli.work import env
from tests.hermes_cli.test_heart_journal import DAY, NOW, STAMP, database, invoke, sandbox, write

__all__ = ["sandbox"]


def test_weekly_counts_paths_changes_and_hides_journal_prose(sandbox, tmp_path):
    database(sandbox["alpha"])
    write()
    write("--via", "review")
    journal = sandbox["alpha"] / "heart/journal.jsonl"
    before = journal.read_bytes()
    changes = sandbox["alpha"] / "heart/changes.jsonl"
    storage.append(changes, Change(ts=STAMP, added=("Changed greeting",), removed=("Old greeting",)))
    storage.append(changes, Change(ts=STAMP, refused="protected-topic"))
    storage.append(changes, Change(ts=STAMP, changed=0))
    storage.append(changes, Change(ts=STAMP, undo="backup.md"))
    output = tmp_path / "weekly"
    code, stdout, _ = invoke("weekly", "--week-ending", DAY, "--out-dir", str(output))
    assert code == 0
    path = output / (DAY.replace("-", "")[2:] + ".md")
    page = path.read_text()
    assert str(path) in stdout
    assert "Journal: 2 lines (turn 1, review 1). Changes: 1, unchanged 1, refused 1, undone 1." in page
    assert "Changed greeting" in page and "Old greeting" in page
    assert "protected-topic 1" in page
    assert "## Alpha" in page and "## Beta\n- Not enabled yet." in page
    for secret in ("Synthetic event", "Synthetic note", "calm"):
        assert secret not in page
    assert journal.read_bytes() == before
    assert path.stat().st_mode & 0o777 == 0o600


def test_weekly_bounds_and_legacy_path_default(sandbox, tmp_path):
    database(sandbox["alpha"])
    write()
    path = sandbox["alpha"] / "heart/journal.jsonl"
    entry = json.loads(path.read_text())
    entry.pop("via")
    path.write_text(json.dumps(entry) + "\n")
    for offset in (-30 * 86400, 3 * 86400):
        storage.append(path, Entry.model_validate({**entry, "ts": storage.stamp(NOW + offset)}))
    invoke("weekly", "--out-dir", str(tmp_path / "out"))
    page = next((tmp_path / "out").glob("*.md")).read_text()
    assert "Journal: 1 lines (turn 1, review 0)" in page


@pytest.mark.parametrize("configured", [False, True])
def test_weekly_default_directory_is_lina_root_relative(sandbox, configured):
    if configured:
        env.current().config["heart"] = {"weekly_out_dir": "private-weekly"}
    assert invoke("weekly")[0] == 0
    directory = sandbox["default"] / ("private-weekly" if configured else "heart-weekly")
    assert len(list(directory.glob("*.md"))) == 1


@pytest.mark.parametrize("wrapper", [None, "profiles", "rows", "stats", "items", "mapping"])
def test_weekly_optional_insight_stats_shapes(sandbox, tmp_path, wrapper):
    row = {"profile": "alpha", "open": 1, "closed": 2, "answered": 2, "맞다": 1, "틀리다": 1, "accuracy": 0.5}
    payload = [row] if wrapper is None else ({"alpha": row} if wrapper == "mapping" else {wrapper: [row]})
    stats = tmp_path / "stats.json"
    stats.write_text(json.dumps(payload))
    output = tmp_path / "out"
    assert invoke("weekly", "--out-dir", str(output), "--insight-stats", str(stats))[0] == 0
    page = next(output.glob("*.md")).read_text()
    assert "Accuracy 50%" in page
    assert "Insights 3: open 1, closed 2" in page


def test_weekly_korean_locale_and_dynamic_agent_names(sandbox, tmp_path):
    env.current().locale = "ko"
    env.current().agents["alpha"] = "테스트 에이전트"
    assert invoke("weekly", "--out-dir", str(tmp_path / "out"))[0] == 0
    page = next((tmp_path / "out").glob("*.md")).read_text()
    assert "테스트 에이전트" in page
    assert "셀레네" not in page


def test_root_yaml_heart_settings_follow_work_environment_loader(sandbox):
    root = sandbox["default"]
    (root / "config.yaml").write_text("heart:\n  weekly_out_dir: custom-weekly\n  owner_sources: [desktop]\nwork:\n  locale: ko\n  owner: {id: person, name: Taylor}\n")
    env.bind(env.load(root))
    assert storage.settings().owner_sources == ("desktop",)
    assert env.current().owner_name == "Taylor"
    assert invoke("weekly")[0] == 0
    assert list((root / "custom-weekly").glob("*.md"))
