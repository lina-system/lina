"""Which profile the host-wide board-lines writer calls the model as (lina#702): ``worker.board_lines_profile`` in the
LINA root's config. On a multiplexed gateway with the key unset there is no profile to call as, so a pass makes no
call and logs one line naming the key (not a traceback per field per pass); with the key set, the call runs in that
profile's secret scope and reads that profile's ``auxiliary.board_lines``."""

from __future__ import annotations

import logging
import sqlite3
import threading
from types import SimpleNamespace

import pytest

from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")
LOGGER = "hermes_cli.lina_board_plain"


class Model:
    """Stands in for ``call_llm``: resolves a credential the way a provider does (``get_secret``), records the
    ``board_lines`` model and the key it saw, or raises what the resolution raises."""

    def __init__(self):
        self.calls: list[dict] = []
        self.fail: Exception | None = None

    def __call__(self, task=None, *, messages, **_kwargs):
        from agent.auxiliary_task_config import _get_auxiliary_task_config
        from agent.secret_scope import get_secret
        from hermes_cli.lina_board_plain import _PROMPTS
        if self.fail is not None:
            raise self.fail
        key = get_secret("OPENROUTER_API_KEY")
        self.calls.append({"task": task, "model": _get_auxiliary_task_config(task).get("model"), "key": key})
        field = next(name for name, prompt in _PROMPTS.items() if prompt in messages[0]["content"])
        text = {"title": "사진 보관함 백업 점검", "now": "지난주 백업을 확인하는 중", "result": "백업을 확인했다."}[field]
        return SimpleNamespace(choices=[SimpleNamespace(message=SimpleNamespace(content=text))])


@pytest.fixture
def env(tmp_path, monkeypatch):
    from agent import auxiliary_client, secret_scope
    from hermes_cli import kanban_db as kb
    from hermes_cli import kanban_db_connect as kbc
    from hermes_cli import lina_board_plain
    work = WorkEnv(tmp_path, monkeypatch)
    model = Model()
    monkeypatch.setattr(auxiliary_client, "call_llm", model)
    monkeypatch.setattr(lina_board_plain, "_said", {"setting": None, "call": None}, raising=False)
    monkeypatch.setattr(secret_scope, "_MULTIPLEX_ACTIVE", True)  # a multiplexed gateway: get_secret fails closed
    kb.init_db()
    conn = kbc.connect()
    try:
        kb.create_task(conn, title="Weekly photo backup check", goal_mode=True, assignee="alpha")
    finally:
        conn.close()
    return SimpleNamespace(work=work, model=model)


def _passes(env, n: int) -> None:
    from hermes_cli.lina_board_plain import _pass
    from hermes_cli.worker import settings
    for i in range(n):
        env.work.clock[0] = NOW + 60 * i
        _pass(settings.root(env.work.root))


def _lines(env) -> dict:
    from hermes_cli import kanban_db as kb
    from hermes_cli.lina_board_plain import lookup
    with sqlite3.connect(kb.kanban_db_path()) as db:
        ids = [row[0] for row in db.execute("SELECT id FROM tasks")]
    return lookup("card", ids, env.work.root, kb.DEFAULT_BOARD)


def _warnings(caplog) -> list[str]:
    return [r.getMessage() for r in caplog.records if r.name == LOGGER and r.levelno >= logging.WARNING]


def test_unset_profile_on_a_multiplexed_gateway_makes_no_call_and_logs_one_line(env, caplog):
    from hermes_cli.lina_board_plain import writer_state
    caplog.set_level(logging.DEBUG, logger=LOGGER)

    _passes(env, 3)

    assert env.model.calls == []
    warnings = _warnings(caplog)
    assert len(warnings) == 1 and "worker.board_lines_profile" in warnings[0]
    assert not any(r.exc_info for r in caplog.records if r.name == LOGGER)
    assert all(line == {"title": None, "now": None, "result": None} for line in _lines(env).values())
    assert writer_state(env.work.root, NOW + 120)["state"] == "stopped"


def test_a_profile_that_does_not_exist_is_one_line_naming_the_key(env, caplog):
    env.work.config(extra="worker: {board_lines_profile: nobody}\n")
    caplog.set_level(logging.DEBUG, logger=LOGGER)

    _passes(env, 2)

    assert env.model.calls == []
    warnings = _warnings(caplog)
    assert len(warnings) == 1 and "worker.board_lines_profile" in warnings[0] and "nobody" in warnings[0]


def test_the_configured_profile_s_model_and_secrets_make_the_call(env, caplog):
    profile = env.work.root / "profiles" / "writer"
    profile.mkdir(parents=True)
    (profile / "config.yaml").write_text(
        "auxiliary:\n  board_lines: {provider: openrouter, model: lines-model-a}\n", encoding="utf-8")
    (profile / ".env").write_text("OPENROUTER_API_KEY=key-of-the-writer-profile\n", encoding="utf-8")
    env.work.config(extra="worker: {board_lines_profile: writer}\n")
    caplog.set_level(logging.DEBUG, logger=LOGGER)

    _passes(env, 1)

    assert env.model.calls
    assert all(c == {"task": "board_lines", "model": "lines-model-a", "key": "key-of-the-writer-profile"}
               for c in env.model.calls)
    assert _warnings(caplog) == []
    assert any(line["title"] == "사진 보관함 백업 점검" for line in _lines(env).values())


def test_the_same_call_failure_is_logged_once_not_per_field_per_pass(env, caplog):
    env.work.config(extra="worker: {board_lines_profile: default}\n")
    env.model.fail = ConnectionError("the model is not reachable")
    caplog.set_level(logging.DEBUG, logger=LOGGER)

    _passes(env, 3)

    warnings = [w for w in _warnings(caplog) if "the call failed" in w]
    assert len(warnings) == 1


def test_the_writer_thread_on_a_multiplexed_gateway_raises_nothing(env, caplog):
    """The gateway's start path: one pass on the writer's own thread with no secret scope installed."""
    from gateway.run_bootstrap import _start_board_lines_writer
    caplog.set_level(logging.DEBUG, logger=LOGGER)
    stop = threading.Event()
    stop.set()

    thread = _start_board_lines_writer(stop)
    thread.join(30)

    assert not thread.is_alive()
    assert env.model.calls == []
    assert not any(r.exc_info for r in caplog.records if r.name == LOGGER)
    assert len(_warnings(caplog)) == 1
