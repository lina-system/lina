"""The store's check that Honcho has its model keys and reaches its model endpoints (lina#650).

The container exec is faked: the fake answers the probe's arguments (``<base>/models`` URLs) from a table of
what each address does, the way the probe inside the api container would.
"""

from __future__ import annotations

import json
import subprocess

import pytest

from hermes_cli import store as st
from tests.hermes_cli.test_store import FakeDocker, make_store

REFUSED = "[Errno 111] Connection refused"


class FakeExec:
    """``docker compose ps`` and ``docker compose exec honcho-api python -c <probe> <url>...``."""

    def __init__(self, answers=None, running=("db", "honcho-api", "honcho-deriver")):
        self.answers, self.running, self.probes = answers or {}, set(running), []

    def __call__(self, argv, *, env=None, capture=False, stdout=None, input=None):
        if "ps" in argv:
            return subprocess.CompletedProcess(argv, 0, "\n".join(sorted(self.running)), "")
        assert "exec" in argv and "honcho-api" in argv and st.MODELS_PROBE in argv
        urls = argv[argv.index(st.MODELS_PROBE) + 1:]
        self.probes.append(urls)
        return subprocess.CompletedProcess(argv, 0, "".join(json.dumps(self.answers.get(url, "")) + "\n"
                                                            for url in urls), "")


@pytest.fixture
def honcho_src(tmp_path, monkeypatch):
    src = tmp_path / "services" / "honcho"
    src.mkdir(parents=True)
    (src / "Dockerfile").write_text("FROM scratch\n", encoding="utf-8")
    monkeypatch.setattr(st, "HONCHO_DIR", src)


def store_with(tmp_path, honcho_env: str, run) -> st.Store:
    directory = tmp_path / "store"
    directory.mkdir()
    (directory / "honcho.env").write_text(honcho_env, encoding="utf-8")
    st.ensure_env(directory)
    return st.Store(directory, run=run, out=lambda line: None)


MAGPIE_ENV = ("LLM_OPENAI_API_KEY=k\nLLM_OPENAI_BASE_URL=http://172.18.0.1:3425/v1\n"
              "EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL=http://172.18.0.1:3425/v1/\n")


def test_reachable_endpoints_are_asked_in_one_exec_and_give_no_line(tmp_path):
    run = FakeExec()
    assert store_with(tmp_path, MAGPIE_ENV, run).model_problems() == []
    assert run.probes == [["http://172.18.0.1:3425/v1/models", "http://172.18.0.1:3425/v1/models"]]


def test_a_refused_endpoint_is_one_line_naming_its_variable(tmp_path):
    env = MAGPIE_ENV.replace("EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL=http://172.18.0.1",
                             "EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL=http://user:pw@127.0.0.1")
    run = FakeExec({"http://user:pw@127.0.0.1:3425/v1/models": REFUSED})
    assert store_with(tmp_path, env, run).model_problems() == [
        f"Honcho cannot reach EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL (127.0.0.1:3425/v1/models: {REFUSED})"]


def test_missing_keys_are_named_with_the_callers_that_need_them(tmp_path):
    env = ("DERIVER_MODEL_CONFIG__TRANSPORT=anthropic\nDREAM_ENABLED=false\n"
           "EMBEDDING_MODEL_CONFIG__OVERRIDES__API_KEY_ENV=EMBEDDING_CUSTOM_API_KEY\n")
    run = FakeExec()
    problems = store_with(tmp_path, env, run).model_problems()
    assert problems == [
        "LLM_OPENAI_API_KEY (or EMBEDDING_CUSTOM_API_KEY, named by EMBEDDING_MODEL_CONFIG__OVERRIDES__API_KEY_ENV) "
        "is not set in honcho.env (embedding cannot call a model)",
        "LLM_ANTHROPIC_API_KEY is not set in honcho.env (deriver cannot call a model)",
        "LLM_OPENAI_API_KEY is not set in honcho.env (summary, dialectic minimal, dialectic low, dialectic medium, "
        "dialectic high, dialectic max cannot call a model)"]
    assert run.probes == []  # no *_BASE_URL set: nothing to ask the container


def test_a_callers_own_key_and_address_replace_the_llm_ones(tmp_path):
    env = ("LLM_OPENAI_API_KEY=k\nLLM_OPENAI_BASE_URL=http://hub:3425/v1\n"
           "SUMMARY_MODEL_CONFIG__OVERRIDES__API_KEY=s\nSUMMARY_MODEL_CONFIG__OVERRIDES__BASE_URL=http://other/v1\n")
    run = FakeExec({"http://other/v1/models": "timed out"})
    assert store_with(tmp_path, env, run).model_problems() == [
        "Honcho cannot reach SUMMARY_MODEL_CONFIG__OVERRIDES__BASE_URL (other/v1/models: timed out)"]
    assert run.probes == [["http://hub:3425/v1/models", "http://other/v1/models"]]


def test_install_ends_with_the_model_check(tmp_path, honcho_src):
    docker, lines = FakeDocker(), []
    st.install(make_store(tmp_path, docker, lines))
    running = next(i for i, line in enumerate(lines) if line.startswith("The store is running"))
    assert lines[running + 1:] == [
        "Model endpoints: LLM_OPENAI_API_KEY is not set in honcho.env (embedding, deriver, summary, dream deduction, "
        "dream induction, dialectic minimal, dialectic low, dialectic medium, dialectic high, dialectic max cannot "
        "call a model)"]


def test_upgrade_ends_with_the_model_check(tmp_path, honcho_src):
    docker, lines = FakeDocker(volume_exists=True), []
    store = make_store(tmp_path, docker, lines)
    st.ensure_env(store.directory)
    (store.directory / "honcho.env").write_text("LLM_OPENAI_API_KEY=k\n", encoding="utf-8")
    docker.running = {"db", "honcho-api", "honcho-deriver"}
    st.install(store)
    assert lines[-2].startswith("Upgrade done.")
    assert lines[-1] == "Model endpoints: Honcho has its keys and reaches every *_BASE_URL in honcho.env."


def test_doctor_shows_the_check_for_a_running_store_only(tmp_path, monkeypatch, capsys):
    from hermes_cli.doctor_state import _check_store_models
    directory = store_with(tmp_path, MAGPIE_ENV, None).directory
    monkeypatch.setattr(st, "store_dir", lambda: directory)
    fake = FakeExec({"http://172.18.0.1:3425/v1/models": REFUSED})
    monkeypatch.setattr(st, "_run_process", lambda argv, **kw: fake(argv))

    finding = _check_store_models(False)
    out = capsys.readouterr().out
    assert "Memory Store" in out
    assert "Honcho cannot reach EMBEDDING_MODEL_CONFIG__OVERRIDES__BASE_URL" in out
    assert "Honcho cannot reach LLM_OPENAI_BASE_URL" in out
    assert len(finding.manual_issues) == 1

    fake.running = {"db"}
    assert _check_store_models(False).manual_issues == []
    assert capsys.readouterr().out == ""
