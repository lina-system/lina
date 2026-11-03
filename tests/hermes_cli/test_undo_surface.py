"""Real package-main transcript with a private temporary LINA home."""

import hashlib

pytest_plugins = ["tests.hermes_cli.test_undo_support"]


def test_real_surface_restores_original_sha256(undo):
    # Given a binary file in the synthetic home.
    original = hashlib.sha256(undo.memory.read_bytes()).hexdigest()
    change = undo.begin(undo.memory)
    undo.memory.write_bytes(b"replacement\r\n\x00\xff")
    # When driving the public package commands.
    undo.ok("end", change)
    latest = undo.ok("last", "--bot", "alpha")
    applied = undo.ok("apply", change)
    # Then the real checkpoint store restores the original digest.
    assert change in latest
    assert "inverse change_id=" in applied
    assert hashlib.sha256(undo.memory.read_bytes()).hexdigest() == original
