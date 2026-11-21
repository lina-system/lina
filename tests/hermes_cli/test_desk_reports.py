"""Desk report registration and a single scheduled sync pass on synthetic source files."""

import argparse
from contextlib import closing

import pytest

import hermes_state_reports as reports
from hermes_cli.subcommands.desk import build_desk_parser


@pytest.fixture
def desk(tmp_path, monkeypatch):
    from hermes_cli import desk as desk_config
    from hermes_constants import get_default_hermes_root

    root = tmp_path / "Desk"
    (root / "2_Areas").mkdir(parents=True)
    (root / "1_Goals").mkdir()
    monkeypatch.setattr(desk_config, "get_desk_path", lambda: root)
    config = tmp_path / "config"
    monkeypatch.setenv("XDG_CONFIG_HOME", str(config))
    legacy = config / "sion-board" / "report-deny"
    legacy.parent.mkdir(parents=True)
    legacy.write_text("*/legacy/*\n", encoding="utf-8")
    policy = get_default_hermes_root() / "report-deny"
    policy.parent.mkdir(parents=True, exist_ok=True)
    policy.write_text("*/restricted/*\n", encoding="utf-8")
    return root, policy


def run(*args):
    parser = argparse.ArgumentParser()
    build_desk_parser(parser.add_subparsers())
    parsed = parser.parse_args(["desk", *args])
    return parsed.func(parsed)


def register(root, capsys):
    doc = root / "2_Areas" / "demo.md"
    doc.write_text("# First\n\nBody", encoding="utf-8")
    assert run("register", "2_Areas/demo.md") == 0
    url = capsys.readouterr().out.strip()
    return doc, url.split("/")[-2], url


def test_register_refreshes_the_same_page(desk, capsys):
    doc, report_id, url = register(desk[0], capsys)
    doc.write_text("# Second\n\nEdited", encoding="utf-8")
    assert run("register", str(doc)) == 0
    assert capsys.readouterr().out.strip() == url
    with closing(reports.owner_connection()) as conn:
        assert reports.get(conn, report_id).body == "# Second\n\nEdited"
        assert reports.get(conn, report_id, 1).title == "First"


@pytest.mark.parametrize(("kind", "reason"), [
    ("outside", "allowed"), ("restricted", "deny"), ("legacy", "deny"), ("marked", "personal"), ("link", "link"),
])
def test_register_refuses_without_copying(desk, capsys, kind, reason):
    root, _ = desk
    outside = root.parent / "outside.md"
    outside.write_text("outside body", encoding="utf-8")
    doc = root / "2_Areas" / kind / "demo.md"
    doc.parent.mkdir()
    if kind == "link":
        doc.symlink_to(outside)
    elif kind == "outside":
        doc = outside
    else:
        doc.write_text("# Restricted", encoding="utf-8")
        if kind == "marked":
            (doc.parent / ".lina-personal").touch()
    assert run("register", str(doc)) == 2
    assert reason in capsys.readouterr().err.lower()
    with closing(reports.owner_connection()) as conn:
        assert conn.execute("SELECT count(*) FROM reports.report").fetchone() == (0,)


@pytest.mark.parametrize("change", ["changed", "moved", "gone", "denied", "error"])
def test_sync_updates_each_source_independently(desk, capsys, change):
    root, policy = desk
    doc, report_id, url = register(root, capsys)
    if change == "changed":
        doc.write_text("# Updated", encoding="utf-8")
    elif change == "moved":
        doc = doc.rename(root / "1_Goals" / "renamed.md")
        doc.write_text("# After move", encoding="utf-8")
    elif change == "gone":
        doc.unlink()
    elif change == "denied":
        policy.write_text("2_Areas/demo.md\n", encoding="utf-8")
    else:
        doc.write_bytes(b"\xff")

    assert run("sync-reports") == (1 if change == "error" else 0)
    with closing(reports.owner_connection()) as conn:
        if change in {"denied", "gone"}:
            assert conn.execute("SELECT desk_state FROM reports.report WHERE report_id = %s",
                                (report_id,)).fetchone() == (change,)
            assert reports.listing(conn, include_private=True) == []
            assert reports.get(conn, report_id, version=1) is None
        else:
            found = reports.get(conn, report_id)
            expected = {"changed": "# Updated", "moved": "# After move", "error": "# First\n\nBody"}
            assert found.body == expected[change]
            assert found.version == (1 if change == "error" else 2)
    if change == "moved":
        assert run("register", str(doc)) == 0
        assert capsys.readouterr().out.strip().splitlines()[-1] == url
