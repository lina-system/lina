"""lina#590: a config.yaml written before the old proxy was removed still loads. Its ``worker.proxy.kind`` and
``worker.proxy.read``, its top-level ``ocx:`` block and its ``providers.opencodex`` entry are simply not read: no
error, no warning, and the node is a magpie node with the defaults of the keys that remain."""

from __future__ import annotations

import logging

import pytest

OLD_CONFIG = """\
model:
  provider: magpie
  default: claude/claude-opus-5-5
ocx:
  port: 10100
  external: {cli: /opt/old-proxy/bin/cli, data_dir: /opt/old-proxy/data}
providers:
  opencodex:
    base_url: http://127.0.0.1:10100/v1
    key_env: OLD_PROXY_KEY
worker:
  proxy:
    kind: ocx
    read: [ocx, magpie]
    remote_id: hub
"""


@pytest.fixture
def old_root(tmp_path, monkeypatch):
    root = tmp_path / "lina"
    root.mkdir()
    (root / "config.yaml").write_text(OLD_CONFIG, encoding="utf-8")
    monkeypatch.setenv("HERMES_HOME", str(root))
    return root


def test_an_old_config_loads_without_error_or_warning_and_its_removed_keys_are_not_read(old_root, caplog):
    from hermes_cli.config import load_config, validate_config_structure
    from hermes_cli.lina_models import node_proxy
    from hermes_cli.worker import proxy, settings

    with caplog.at_level(logging.WARNING):
        config = load_config()
        issues = validate_config_structure(config)
        node = proxy.of(settings.section(old_root))
        screens = node_proxy(old_root)

    assert issues == []
    assert [record.getMessage() for record in caplog.records if record.levelno >= logging.WARNING] == []
    assert node == screens == proxy.Proxy(remote_id="hub")
    assert not hasattr(node, "kind") and not hasattr(node, "read")
