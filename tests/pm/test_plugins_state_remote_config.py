"""PM's dependency runtime in a Remote Config deployment (#122509): the runtime has no remote
backend client and no plane credential, so reading a home's plugin selection there must neither
fail closed nor reach for the plane. It reads the local file PM publishes to and CAS-checks."""
import pytest

from pm import plugins_state


@pytest.fixture
def remote_home(tmp_path, monkeypatch):
    from hermes_cli import config_backend
    home = tmp_path / "home"
    home.mkdir()
    (home / "config.yaml").write_text("plugins:\n  enabled: [alpha]\n", encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(home))
    monkeypatch.setenv(config_backend.BACKEND_ENV, "remote")
    monkeypatch.delenv("HERMES_CONFIG_INSTANCE_ID", raising=False)
    monkeypatch.setattr(config_backend, "_BOOTSTRAPPED", True)
    yield home


def test_pm_runtime_reads_the_local_selection_without_the_plane(remote_home, monkeypatch):
    import pm.runtime
    monkeypatch.setattr(pm.runtime, "is_runtime", lambda: True)
    assert plugins_state.read_home_selection(remote_home)["plugins"]["enabled"] == ["alpha"]
    assert plugins_state.read_home_selection(remote_home / "absent") is None


def test_application_process_reads_the_selection_through_the_backend(remote_home):
    # Control: outside PM's runtime the selected (unconfigured) remote backend is consulted and
    # fails closed rather than serving the local file.
    from hermes_cli.config_backend import ConfigBackendUnavailable
    with pytest.raises(ConfigBackendUnavailable):
        plugins_state.read_home_selection(remote_home)
