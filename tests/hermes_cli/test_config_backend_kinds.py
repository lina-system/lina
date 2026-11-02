"""File backend defaults and unavailable backend selectors fail closed."""
from __future__ import annotations


import pytest

from hermes_cli import config_backend as cb

def test_file_backend_protocol_defaults_are_no_ops(tmp_path):
    fb = cb.FileBackend()
    assert fb.bootstrap_env_names() == frozenset()
    assert fb.doctor_warnings(tmp_path) == ()
    with fb.preserve_read_base():
        pass


@pytest.mark.parametrize("kind", ["../etc", "remote.backend", "remote x", "os;id", "_remote", "9lives",
                                  "x" * 65, "remote/backend", "plugins.missing_backend"])
def test_kind_is_validated_before_any_import(kind, monkeypatch):
    def no_import(name, *a, **k):
        raise AssertionError(f"imported {name!r} for an invalid kind")
    monkeypatch.setattr(cb.importlib, "import_module", no_import)
    monkeypatch.setattr(cb, "_BOOTSTRAPPED", True)
    monkeypatch.setenv(cb.BACKEND_ENV, kind)
    with pytest.raises(cb.ConfigBackendUnavailable, match="is not a config backend"):
        cb.get_config_backend()


def test_absent_kind_fails_closed(monkeypatch):
    monkeypatch.setattr(cb, "_BOOTSTRAPPED", True)
    monkeypatch.setenv(cb.BACKEND_ENV, "nosuchbackend")
    with pytest.raises(cb.ConfigBackendUnavailable, match="is not a config backend"):
        cb.get_config_backend()
