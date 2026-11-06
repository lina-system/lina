"""``utils.atomic_roundtrip_yaml_update_many`` (lina#256): several dotted keys of a user-edited YAML file in ONE temp
file + atomic replace, with the navigation, comment, order and quoting preservation of ``atomic_roundtrip_yaml_update``."""

from __future__ import annotations

import textwrap

import pytest

import hermes_yaml
import utils

DOC = textwrap.dedent("""\
    # top comment
    model_overrides:
      glm-5.3: {x: 1}   # a literal dotted key
    approvals:
      mode: 'off'       # quoted: YAML 1.1 would read a bare off as false
    keep: last
    """)


@pytest.fixture
def path(tmp_path):
    target = tmp_path / "settings.yaml"
    target.write_text(DOC, encoding="utf-8")
    return target


@pytest.fixture
def replaces(monkeypatch, path):
    real, seen = utils.atomic_replace, []

    def spy(*args, **kwargs):
        seen.append(args[1])
        return real(*args, **kwargs)
    monkeypatch.setattr(utils, "atomic_replace", spy)
    return seen


def test_several_keys_are_one_replacement_and_keep_comments_order_and_quotes(path, replaces):
    utils.atomic_roundtrip_yaml_update_many(path, {"approvals.timeout": 30, "added.deep.key": "v", "keep": None})
    assert len(replaces) == 1
    text = path.read_text(encoding="utf-8")
    assert text.startswith("# top comment\n")
    assert "  glm-5.3: {x: 1}   # a literal dotted key" in text
    assert "  mode: 'off'       # quoted: YAML 1.1 would read a bare off as false" in text
    assert text.index("model_overrides:") < text.index("approvals:") < text.index("added:")
    data = hermes_yaml.safe_load(text)
    assert data["approvals"] == {"mode": "off", "timeout": 30} and data["added"] == {"deep": {"key": "v"}}
    assert "keep" not in data


def test_escaped_dots_and_existing_literal_dotted_keys_are_honoured(path, replaces):
    utils.atomic_roundtrip_yaml_update_many(path, {"providers.qwen3\\.5.ctx": 8, "model_overrides.glm-5.3.x": 2})
    data = hermes_yaml.safe_load(path.read_text(encoding="utf-8"))
    assert data["providers"] == {"qwen3.5": {"ctx": 8}}
    assert data["model_overrides"] == {"glm-5.3": {"x": 2}}  # no glm-5: {"3": ...} phantom sibling
    assert len(replaces) == 1


def test_removing_under_a_missing_parent_alone_writes_nothing(path, replaces):
    utils.atomic_roundtrip_yaml_update_many(path, {"nowhere.at.all": None})
    assert replaces == [] and path.read_text(encoding="utf-8") == DOC
