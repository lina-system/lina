"""lina-owner-rules states the owner's documented defaults; the cron flags it tells the model to pin must
exist on `lina cron create` and `lina cron edit`, and the compaction default it states must be the one
the config ships."""

from __future__ import annotations

import argparse
import re

from tests.skills._skill_contract import assert_cli_commands_exist, assert_contract, assert_tools_registered, load

SKILL = load("lina-owner-rules")


def test_document_contract():
    assert_contract(SKILL, complete_sections=True)


def _subparser(parser: argparse.ArgumentParser, name: str) -> argparse.ArgumentParser:
    action = next(a for a in parser._actions if isinstance(a, argparse._SubParsersAction))
    return action.choices[name]


def test_cron_pin_flags_exist_on_create_and_edit():
    from hermes_cli.main import _build_cli_parser

    assert_tools_registered(SKILL, ("terminal",))
    assert {"cron create", "cron edit"} <= set(assert_cli_commands_exist(SKILL))
    cron = _subparser(_build_cli_parser()[0], "cron")
    for command in ("create", "edit"):
        flags = {flag for action in _subparser(cron, command)._actions for flag in action.option_strings}
        assert {"--model", "--reasoning-effort"} <= flags, command
    assert "--model <the Sonnet model> --reasoning-effort medium" in SKILL.body


def test_stated_compaction_default_is_the_shipped_default():
    from hermes_cli.config_defaults import DEFAULT_CONFIG

    stated = re.search(r"`compression\.threshold: ([0-9.]+)`", SKILL.body)
    assert stated, "the skill no longer states the compaction default"
    assert float(stated.group(1)) == DEFAULT_CONFIG["compression"]["threshold"]
