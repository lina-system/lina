"""Work settings for ACP runners: ``type: acp`` takes a mandatory non-empty ``argv`` of non-empty texts and an
optional ``flavor``; nothing else about the runner schema changes."""

from __future__ import annotations

import pytest

ENTRY_POINTS = {
    "grok": ["grok", "agent", "stdio"],
    "dsh": ["dsh", "acp"],
    "agy": ["agy", "acp"],
    "claude": ["claude-agent-acp"],
    "codex": ["codex-acp"],
}


def _doc(runner: dict) -> dict:
    return {"worker": {"default_runner": "omo", "runners": {"acp": runner}}}


def _paths(doc: dict) -> list[str]:
    from hermes_cli.lina_work_settings import validate
    return [e["path"] for e in validate(doc)]


@pytest.mark.parametrize("flavor", ENTRY_POINTS)
def test_each_flavor_with_its_entry_point_is_accepted(flavor):
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "flavor": flavor, "argv": ENTRY_POINTS[flavor]})) == []


def test_flavor_is_optional_and_omitted_means_generic():
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "argv": ["some-agent", "acp"]})) == []


@pytest.mark.parametrize("argv", [None, [], [""], ["agent", ""], "agent acp", ["agent", 1]],
                         ids=["missing", "empty", "empty-text", "one-empty-text", "string", "non-text"])
def test_argv_must_be_a_non_empty_list_of_non_empty_texts(argv):
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "flavor": "dsh", "argv": ["dsh", "acp"]})) == []
    runner = {"type": "acp", "flavor": "dsh"}
    if argv is not None:
        runner["argv"] = argv
    assert "worker.runners.acp.argv" in _paths(_doc(runner))


@pytest.mark.parametrize("flavor", ["gemini", "", "Grok", 7, ["grok"]])
def test_an_unknown_or_mistyped_flavor_is_refused_with_its_path(flavor):
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "flavor": "grok", "argv": ["grok", "agent", "stdio"]})) == []
    errors = _paths(_doc({"type": "acp", "flavor": flavor, "argv": ["x"]}))
    assert "worker.runners.acp.flavor" in errors


def test_flavor_belongs_to_acp_runners_only():
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "argv": ["codex-acp"], "flavor": "codex"})) == []
    doc = _doc({"type": "command", "argv": ["codex", "exec", "-"], "flavor": "codex"})
    assert "worker.runners.acp.flavor" in _paths(doc)


def test_the_acp_schema_adds_no_bypass_or_permission_keys():
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "argv": ["x"]})) == []
    for key in ("permissions", "bypass", "yolo", "client_fs", "terminal"):
        doc = _doc({"type": "acp", "argv": ["x"], key: True})
        assert f"worker.runners.acp.{key}" in _paths(doc)


def test_access_still_validates_on_an_acp_runner():
    from hermes_cli.lina_work_settings import validate
    assert validate(_doc({"type": "acp", "argv": ["x"], "access": "read-only"})) == []
    assert "worker.runners.acp.access" in _paths(_doc({"type": "acp", "argv": ["x"], "access": "root"}))
