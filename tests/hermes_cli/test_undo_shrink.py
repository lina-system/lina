"""Shrink warning boundary cases, using reference metadata only."""

import json

import pytest

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


@pytest.mark.parametrize("before,after,warn", [(250, 0, True), (400, 20, True), (400, 200, False),
                                               (100, 5, False), (300, None, True), (200, 20, False)])
def test_shrink_warning_boundaries(undo, before, after, warn):
    # Given captured bytes and an intentional shrink/deletion.
    undo.rule.write_bytes(b"x" * before)
    change = undo.begin(undo.rule)
    if after is None:
        undo.rule.unlink()
    else:
        undo.rule.write_bytes(b"y" * after)
    # When closing the batch.
    code, _, error = undo.run("end", change)
    # Then it succeeds and records only the relevant byte counts.
    assert code == 0
    ref = undo.journal()[0]["objects"][0]
    assert bool(ref["shrunk"]) == warn
    assert bool(error) == warn
    if warn:
        assert ref["shrunk"] == {"before": before, "after": after}


def test_shrink_display_and_inverse_drop_marker(undo):
    # Given a truncated file batch.
    undo.rule.write_bytes(b"x" * 250)
    change = undo.begin(undo.rule)
    undo.rule.write_bytes(b"")
    undo.ok("end", change)
    # When restoring it.
    undo.ok("apply", change)
    # Then original rows are flagged but the inverse is not.
    assert undo.ok("show", change).startswith("!")
    inverse = undo.journal()[-1]
    assert inverse["objects"][0]["shrunk"] is None
    assert not undo.ok("show", inverse["change_id"]).startswith("!")


@pytest.mark.parametrize("sizes", [{"before": 0, "after": 0}, {"before": 10}, {"before": "10", "after": 1},
                                    {"before": 10, "after": -1}, {"before": 10, "after": 1, "content": "x"}])
def test_malformed_shrink_rejected(undo, sizes):
    # Given malformed byte-count metadata; when logging it.
    code, _, _ = undo.log([{"type": "file", "path": str(undo.rule), "sha256": None,
                            "after": None, "shrunk": sizes}])
    # Then it cannot enter the journal.
    assert code == 2 and undo.journal() == []
