"""Generated field text is accepted, rewritten once, or dropped (lina#36)."""

import subprocess

import pytest

from tests.hermes_cli._work_env import WorkEnv


@pytest.mark.parametrize("answers,expected,calls", [
    (["알림이 빠짐없이 알려지게"], "알림이 빠짐없이 알려지게\n", 1),
    (["(급함) #19 넘기려고", "알림이 빠짐없이 알려지게"], "알림이 빠짐없이 알려지게\n", 2),
    (["(급함) #19 넘기려고", "(다시) #19 넘기려고"], "", 2),
])
def test_plain_rewrites_only_once_when_generator_breaks_field_rules(tmp_path, monkeypatch, answers, expected, calls):
    # Given
    work = WorkEnv(tmp_path, monkeypatch)
    from hermes_cli.work import cmd_read
    prompts = []

    def generate(argv, **kwargs):
        prompts.append(kwargs["input"])
        return subprocess.CompletedProcess(argv, 0, answers[min(len(prompts), len(answers)) - 1], "")

    monkeypatch.setattr(cmd_read.subprocess, "run", generate)
    # When
    output = work.ok("plain", "왜", "--context", "알림 점검", "--", "synthetic-generator")
    # Then
    assert output == expected
    assert len(prompts) == calls
