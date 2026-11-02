"""A cron prompt is prose: the referenced-script walk reads only its shell-context lines.

Regression for #68: ``... (first-party). Sunday ...`` tokenized as ``. Sunday`` and a line starting
with a path read as executing it; from a cloud-synced working directory the guard then refused the
job, while the same prompt passed from home. Real shell lines in a prompt are still walked.
"""

import pytest

from cron.lifecycle_guard import GatewayLifecycleBlocked, check_gateway_lifecycle

PROSE_PROMPTS = [
    "Summarize the release notes (first-party). Sunday is the cutoff.",
    "Collect the vendor list.\nKeep only official sources (first-party). Sunday reports go out at noon.",
    "~/notes/a.md lists the sources to read.\nSummarize each one in two sentences.",
    "notes/a.md lists the sources to read. Summarize them.",
    "Read the source code under ~/src and report any TODOs.",
]


def _workdir(tmp_path, monkeypatch, location):
    if location == "cloud":
        workdir = tmp_path / "Library" / "CloudStorage" / "Drive" / "work"
    else:
        workdir = tmp_path / "home"
    (workdir / "notes").mkdir(parents=True)
    (workdir / "notes" / "a.md").write_text("- https://example.com\n", encoding="utf-8")
    monkeypatch.chdir(workdir)
    monkeypatch.setenv("HOME", str(workdir))
    return workdir


@pytest.mark.parametrize("location", ["cloud", "home"])
@pytest.mark.parametrize("prompt", PROSE_PROMPTS)
def test_prose_prompt_passes_from_any_working_directory(tmp_path, monkeypatch, location, prompt):
    _workdir(tmp_path, monkeypatch, location)
    check_gateway_lifecycle(prompt, None)


def _restart_script(directory):
    script = directory / "restart.sh"
    script.write_text("#!/bin/sh\nhermes gateway restart\n", encoding="utf-8")
    return script


@pytest.mark.parametrize("prompt_template", [
    "Run the weekly maintenance.\n. ./restart.sh",
    "Run the weekly maintenance.\nsource ~/restart.sh",
    'bash -c "{script}"',
    'bash -c "echo starting\n{script}"',
    "sh {script}",
    "sudo ./restart.sh",
    "cd {dir} && ./restart.sh",
    "$ ./restart.sh",
    "Run this:\n```\n./restart.sh\n```",
    "Run this:\n```bash\n$ ./restart.sh\n```",
])
def test_shell_lines_in_prompt_are_still_walked(tmp_path, monkeypatch, prompt_template):
    script = _restart_script(tmp_path)
    monkeypatch.chdir(tmp_path)
    monkeypatch.setenv("HOME", str(tmp_path))
    prompt = prompt_template.format(script=script, dir=tmp_path)
    with pytest.raises(GatewayLifecycleBlocked, match="contains a gateway lifecycle command"):
        check_gateway_lifecycle(prompt, None)


def test_sourced_cloud_script_in_prompt_is_still_refused_unread(tmp_path, monkeypatch):
    cloud = tmp_path / "Library" / "CloudStorage" / "Drive"
    cloud.mkdir(parents=True)
    monkeypatch.chdir(tmp_path)
    with pytest.raises(GatewayLifecycleBlocked, match="could not scan"):
        check_gateway_lifecycle(f"Nightly sync.\nsource {cloud / 'sync.sh'}", None)


def test_lifecycle_command_in_prose_is_still_blocked(tmp_path, monkeypatch):
    monkeypatch.chdir(tmp_path)
    with pytest.raises(GatewayLifecycleBlocked):
        check_gateway_lifecycle("Every night (first-party). Then run hermes gateway restart.", None)
