"""A migrated agent's terminal starts scoped to the copy's own home, never the host user's (lina#706)."""

import json

from hermes_cli.migrate_memory import _publish_config


def test_copy_terminal_starts_in_the_copy_home(tmp_path):
    from hermes_constants import get_subprocess_home
    from tools.terminal_scope import build_profile_terminal_scope

    copied = tmp_path / "snapshot/profile/config.yaml"
    copied.parent.mkdir(parents=True)
    copied.write_text(json.dumps({"memory": {"memory_enabled": True}}))
    before = copied.read_bytes()
    destination = tmp_path / "lina/profiles/alpha-copy"
    destination.mkdir(parents=True)

    _publish_config(copied, destination, lcm_store=False)

    # The per-profile terminal policy the gateway binds for the copy starts its terminal in the copy.
    assert build_profile_terminal_scope(destination)["TERMINAL_CWD"] == str(destination)
    # HOME for the copy's tool subprocesses is the copy's own home directory.
    from hermes_cli.config_backend import FileBackend
    terminal = FileBackend().read_user_doc(destination)["terminal"]
    env = {"HERMES_HOME": str(destination), "TERMINAL_HOME_MODE": terminal["home_mode"], "HOME": str(tmp_path)}
    assert get_subprocess_home(env) == str(destination / "home")
    assert terminal["auto_source_bashrc"] is False
    # The copied memory settings survive and the source copy is untouched.
    assert FileBackend().read_user_doc(destination)["memory"] == {"memory_enabled": True}
    assert copied.read_bytes() == before


def test_copy_without_source_config_still_gets_a_scoped_terminal(tmp_path):
    from tools.terminal_scope import build_profile_terminal_scope

    destination = tmp_path / "lina/profiles/alpha-copy"
    destination.mkdir(parents=True)

    _publish_config(None, destination, lcm_store=True)

    assert build_profile_terminal_scope(destination)["TERMINAL_CWD"] == str(destination)
    from hermes_cli.config_backend import FileBackend
    assert FileBackend().read_user_doc(destination)["lcm"] == {"store_backend": "postgres"}
