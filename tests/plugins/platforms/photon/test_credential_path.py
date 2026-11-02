"""Photon writes credentials at the retained exclusion path."""
def test_runtime_record_uses_protected_home_root(tmp_path, monkeypatch):
    from plugins.platforms.photon.adapter import _runtime_record_path
    from hermes_cli.home_data_layout import SECRET_CACHE_FILES
    monkeypatch.setenv("HERMES_HOME", str(tmp_path))
    assert _runtime_record_path() == tmp_path / "photon-sidecar.json"
    assert _runtime_record_path().name in SECRET_CACHE_FILES
