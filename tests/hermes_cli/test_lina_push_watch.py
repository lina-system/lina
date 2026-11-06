"""lina#244: what pushes and when. One scene over a scratch LINA root (a work record, ``lina worker`` runs and kanban
cards); fake devices decrypt every message and check it against ``docs/api/push-client.yaml``."""

from __future__ import annotations

import json
import logging
import stat
import threading

import pytest

from hermes_cli import lina_ocx, lina_push, lina_push_watch
from hermes_cli.lina_push_watch import Sender
from tests.hermes_cli._lina_push_fakes import Device, PushService, throwaway_key
from tests.hermes_cli._work_env import NOW, WorkEnv

pytestmark = pytest.mark.platforms("posix")


@pytest.fixture
def work(tmp_path, monkeypatch):
    env = WorkEnv(tmp_path, monkeypatch)
    monkeypatch.setattr(lina_ocx, "lina_root", lambda: env.root)
    from hermes_cli.worker import hub
    monkeypatch.setattr(hub.Hub, "meta", lambda _hub, job, live=False: {"state": "running"})
    (env.root / "worker" / "hub" / "jobs").mkdir(parents=True)
    env.goal_and_todos(todos=2)
    return env


@pytest.fixture
def key(monkeypatch):
    value = throwaway_key()
    monkeypatch.setenv(lina_push.KEY_ENV, value)
    return value


@pytest.fixture
def device(work, key):
    one = Device("phone")
    lina_push.save_subscription(work.root, one.subscription())
    return one


def _sender(work, service: PushService) -> Sender:
    return Sender(work.root, lina_push.vapid_key()[0], service.post, clock=lambda: work.clock[0])


def _kanban(fn):
    from hermes_cli import kanban_db_connect as kbc
    conn = kbc.connect()
    try:
        return fn(conn)
    finally:
        conn.close()


def _card(conn, title: str) -> str:
    from hermes_cli import kanban_db
    return kanban_db.create_task(conn, title=title, assignee="alpha")


def _job(work, tid: str, state: str, verdict=None) -> None:
    hub = work.root / "worker" / "hub"
    job = {"id": tid, "bot": "alpha", "topic": f"topic-{tid}", "mode": "ulw", "state": state, "created": NOW - 3600,
           "started": NOW - 3000, "host": "here", "prompt": "ulw do it\n", "verdict": verdict, "card": None, "todo": None}
    (hub / "jobs" / f"{tid}.json").write_text(json.dumps(job), encoding="utf-8")
    if state != "running":
        with (hub / "receipts.jsonl").open("a", encoding="utf-8") as fh:
            fh.write(json.dumps({"at": "", "ts": NOW - 600, "task": tid, "kind": "exec_end", "state": state}) + "\n")


def _scene(work) -> dict[str, str]:
    """Everything the board shows, of which only a decision, a reply to the owner and two stopped cards need him."""
    from hermes_cli import kanban_db
    work.ok("add", "할 일", "--by", "owner", "--from", "alpha", "--title", "회의 자료 보기", "--why", "기록용",
            "--done", "확인했다")  # T3: the owner's own to-do
    work.ok("wait", "T1", "--decide", "색을 고를까?")
    work.ok("wait", "G1", "--decide", "범위를 줄일까?")
    work.ok("comment", "G1", "줄이자", "--answer")  # answered: waits for the agent, not for him
    work.ok("comment", "T2", "로그도 남겨 줘")
    work.ok("reply", "T2", "c1", "로그를 남겼다", "--result", "applied")
    work.ok("comment", "T1", "메모: 내일 다시 보자", "--by", "alpha")  # an agent's own comment, no reply to him
    _job(work, "OT-RUN0001", "running")  # progress
    _job(work, "OT-RUN0002", "done", verdict="accepted")  # a finished run
    _job(work, "OT-RUN0003", "done")  # a run waiting for review

    def cards(conn):
        blocked, asking, done, running = (_card(conn, t) for t in ("배포 승인", "범위 질문", "끝난 일", "도는 일"))
        assert kanban_db.block_task(conn, blocked, reason="배포해도 될까요?", kind="needs_input")
        kanban_db.add_comment(conn, asking, "alpha", "질문: 둘 중 어느 쪽으로 갈까요?")
        assert kanban_db.complete_task(conn, done, result="끝", fire_lifecycle_hook=False)
        assert kanban_db.claim_task(conn, running, claimer="alpha")
        kanban_db.add_comment(conn, running, "alpha", "진행: 테스트 돌리는 중")
        return {"blocked": blocked, "asking": asking, "done": done, "running": running}
    return _kanban(cards)


def _inbox_set(work) -> set[str]:
    """The pushed set the issue names, from the functions the bell and ``/work/inbox`` serve."""
    from hermes_cli.lina_board import _notices_in_locale, work_read
    from plugins.kanban.dashboard.work_api import get_inbox
    work_rows = work_read(work.root)
    asks = {n["key"] for n in _notices_in_locale(work.root, work_rows, []) if n["kind"] == "ask"
            and not next(r for r in work_rows["decide"] if r["target"] == n["target"])["answered"]}
    inbox = get_inbox(board=None)
    return asks | {"reply:T2:c1"} | {f"blocked:{c['cardId']}" for c in inbox["blocked"]} | {
        f"question:{q['cardId']}:{q['commentId']}" for q in inbox["questions"]}


def test_the_pushed_set_is_the_inbox_set_and_progress_finished_runs_reviews_and_own_todos_never_push(work, device):
    service = PushService(device)
    sender = _sender(work, service)
    sender.sweep()  # the first start records what is open
    cards = _scene(work)
    sender.sweep()
    pushed = {m["tag"]: m for m in device.got}
    assert set(pushed) == _inbox_set(work)
    assert {m["type"] for m in device.got} == {"show"} and len(device.got) == len(pushed)
    assert {(m["kind"], m["target"]) for m in pushed.values()} == {
        ("ask", "T1"), ("reply", "T2"), ("blocked", cards["blocked"]), ("question", cards["asking"])}
    assert pushed["reply:T2:c1"]["body"] == "로그를 남겼다"
    assert pushed[f"blocked:{cards['blocked']}"]["body"] == "배포해도 될까요?"
    # A card's agent is a profile id; the title names the agent as the record does (a Latin id would blank it).
    assert pushed[f"blocked:{cards['blocked']}"]["title"] == "알파 · 배포 승인"
    assert next(m for m in pushed.values() if m["kind"] == "question")["title"] == "알파 · 범위 질문"


def test_a_changed_item_replaces_its_tag_and_an_item_that_leaves_is_closed_on_every_device(work, device):
    from hermes_cli import kanban_db
    from hermes_cli.lina_board_write import mark_read
    tablet = Device("tablet")
    lina_push.save_subscription(work.root, tablet.subscription())
    service = PushService(device, tablet)
    sender = _sender(work, service)
    sender.sweep()
    cards = _scene(work)
    sender.sweep()
    ask = next(m["tag"] for m in device.got if m["kind"] == "ask")
    question = next(m["tag"] for m in device.got if m["kind"] == "question")
    blocked = f"blocked:{cards['blocked']}"

    def after(change) -> list[dict]:
        device.got.clear()
        tablet.got.clear()
        change()
        sender.sweep()
        assert tablet.got == device.got
        return [{k: m[k] for k in ("type", "tag", "body") if k in m} for m in device.got]

    reworded = after(lambda: work.ok("edit", "T1", "--decide", "파랑으로 할까?", "--why", "더 좁혔다"))
    assert reworded == [{"type": "show", "tag": ask, "body": "파랑으로 할까?"}]  # same tag: the device replaces it
    assert after(lambda: None) == []
    assert after(lambda: mark_read({"keys": [ask]}, now=NOW)) == [{"type": "close", "tag": ask}]
    work.clock[0] += 60
    assert after(lambda: work.ok("comment", "T2", "고마워")) == [{"type": "close", "tag": "reply:T2:c1"}]
    assert after(lambda: _kanban(lambda c: kanban_db.unblock_task(c, cards["blocked"], by="owner"))) == [
        {"type": "close", "tag": blocked}]
    assert after(lambda: _kanban(lambda c: kanban_db.add_comment(c, cards["asking"], "owner", "왼쪽"))) == [
        {"type": "close", "tag": question}]
    assert after(lambda: work.ok("reply", "G1", "c1", "반영했다", "--result", "applied")) == [
        {"type": "show", "tag": "reply:G1:c1", "body": "반영했다"}]  # settles G1's answered decision
    new = after(lambda: work.ok("wait", "G1", "--decide", "마감을 미룰까?"))
    assert [m["type"] for m in new] == ["show"] and new[0]["tag"].startswith("ask:G1:")
    work.clock[0] += 60
    closed = after(lambda: work.ok("comment", "G1", "미루자", "--answer"))  # answered; he also saw the reply
    assert sorted(closed, key=lambda m: m["tag"]) == [{"type": "close", "tag": new[0]["tag"]},
                                                      {"type": "close", "tag": "reply:G1:c1"}]


def test_marking_a_reply_read_closes_it_on_every_device_and_the_mark_survives_later_marks(work, device):
    from hermes_cli.lina_board_write import Refused, current_notices, mark_read
    tablet = Device("tablet")
    lina_push.save_subscription(work.root, tablet.subscription())
    sender = _sender(work, PushService(device, tablet))
    sender.sweep()
    _scene(work)
    sender.sweep()
    ask = next(m["tag"] for m in device.got if m["kind"] == "ask")
    device.got.clear()
    tablet.got.clear()
    marked = mark_read({"keys": ["reply:T2:c1"]}, now=NOW)
    assert marked["unread"] == len(current_notices(work.root, NOW))  # the bell's count is about its own notices
    sender.sweep()
    assert device.got == tablet.got == [{"v": 1, "type": "close", "tag": "reply:T2:c1"}]
    device.got.clear()
    mark_read({"keys": [ask]}, now=NOW)  # pruning to current keys keeps the reply's mark
    sender.sweep()
    assert device.got == [{"v": 1, "type": "close", "tag": ask}]
    with pytest.raises(Refused, match="not a current notice"):
        mark_read({"keys": ["reply:T9:c1"]}, now=NOW)


def test_a_restart_neither_repeats_nor_misses_and_the_first_start_does_not_burst(work, device):
    _scene(work)
    service = PushService(device)
    _sender(work, service).sweep()
    assert device.got == [] and (work.root / "push" / "state.json").is_file()  # open items recorded, not pushed
    _sender(work, service).sweep()
    assert device.got == []  # restarted: nothing again
    work.ok("wait", "T2", "--decide", "로그 보관을 늘릴까?")  # while the core is down
    service.status[device.endpoint] = 503
    _sender(work, service).sweep()
    assert device.got == []
    service.status.clear()
    work.clock[0] += lina_push_watch.BACKOFF_SECONDS[0]
    _sender(work, service).sweep()  # restarted again: the undelivered message is still owed
    assert [(m["type"], m["kind"], m["target"]) for m in device.got] == [("show", "ask", "T2")]
    _sender(work, service).sweep()
    assert len(device.got) == 1


def test_404_and_410_drop_the_subscription_other_failures_back_off_and_log_the_host_only(work, device, caplog):
    gone, flaky = Device("gone"), Device("flaky", "web.push.apple.com")
    for one in (gone, flaky):
        lina_push.save_subscription(work.root, one.subscription())
    service = PushService(device, gone, flaky)
    service.status.update({gone.endpoint: 410, flaky.endpoint: 500})
    sender = _sender(work, service)
    sender.sweep()
    work.ok("wait", "T2", "--decide", "로그 보관을 늘릴까?")
    with caplog.at_level(logging.INFO, logger="hermes_cli"):
        sender.sweep()
        posts = len(service.headers)
        sender.sweep()  # inside the backoff: the failing device is not tried again
        assert len(service.headers) == posts
    assert [m["target"] for m in device.got] == ["T2"] and gone.got == flaky.got == []
    left = {s["endpoint"] for s in lina_push.subscriptions(work.root, NOW).values()}
    assert left == {device.endpoint, flaky.endpoint}
    state = json.loads((work.root / "push" / "state.json").read_text(encoding="utf-8-sig"))
    assert state["devices"][lina_push.sub_id(flaky.endpoint)]["retryAt"] == NOW + lina_push_watch.BACKOFF_SECONDS[0]
    assert "web.push.apple.com answered 500" in caplog.text and "fcm.googleapis.com answered 410" in caplog.text
    assert "/fcm/send/" not in caplog.text and "flaky" not in caplog.text
    assert sender.retry_at == NOW + lina_push_watch.BACKOFF_SECONDS[0]
    service.status.clear()
    work.clock[0] = sender.retry_at
    sender.sweep()
    assert [m["target"] for m in flaky.got] == ["T2"] and sender.retry_at == 0


def test_the_key_never_appears_in_responses_logs_the_work_record_or_the_push_state(work, key, caplog):
    from fastapi.testclient import TestClient

    from hermes_cli import web_server
    client = TestClient(web_server.app)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    phone = Device("phone")
    service = PushService(phone)
    with caplog.at_level(logging.DEBUG):
        bodies = [client.get("/api/lina/push/key").text,
                  client.post("/api/lina/push/subscriptions", json=phone.subscription()).text]
        sender = _sender(work, service)
        sender.sweep()
        _scene(work)
        sender.sweep()
        service.status[phone.endpoint] = 500
        work.ok("wait", "T2", "--decide", "로그 보관을 늘릴까?")
        sender.sweep()
        bodies.append(client.post("/api/lina/push/subscriptions/remove", json={"endpoint": phone.endpoint}).text)
    assert phone.got and (work.root / "push" / "state.json").is_file()
    raw = lina_push.unb64u(key)
    files = [p for p in work.root.rglob("*") if p.is_file()]
    assert any(p.parent.name == "push" for p in files) and any(p.suffix in (".db", ".sqlite") for p in files)
    for path in files:
        data = path.read_bytes()
        assert key.encode() not in data and raw not in data, path
    for text in bodies + [caplog.text] + [json.dumps(h) for h in service.headers]:
        assert key not in text
    assert repr(lina_push.vapid_key()[0]).count(key) == 0


def test_the_started_sender_wakes_on_a_record_write_long_before_its_sweep(work, device, monkeypatch):
    service = PushService(device)
    pushed, seeded = threading.Event(), threading.Event()

    class Watched(Sender):
        def __init__(self, root, vapid):
            super().__init__(root, vapid, self.post_and_tell)

        def post_and_tell(self, url, headers, body):
            status = service.post(url, headers, body)
            pushed.set()
            return status

        def sweep(self):
            super().sweep()
            seeded.set()
    monkeypatch.setattr(lina_push_watch, "Sender", Watched)
    assert lina_push_watch.SWEEP_SECONDS >= 60
    halt = lina_push_watch.start(work.root)
    try:
        assert seeded.wait(30)
        work.ok("wait", "T2", "--decide", "로그 보관을 늘릴까?")  # one row in the work record's event table
        assert pushed.wait(30)
    finally:
        halt()
    assert [(m["kind"], m["target"]) for m in device.got] == [("ask", "T2")]


def _lifespan(monkeypatch, which: str):
    """One of the two web server lifespans, with the standalone one's unrelated boot services stubbed out."""
    from hermes_cli import web_server as web
    from tui_gateway import methods_groups
    for name in ("_warm_gateway_module", "_eager_reconcile_own_session_db", "_start_desktop_cron_ticker",
                 "_terminate_desktop_managed_gateway"):
        monkeypatch.setattr(web, name, lambda *a, **k: None)
    for name in ("start_hosted_room_service", "stop_hosted_room_service"):
        monkeypatch.setattr(methods_groups, name, lambda *a, **k: None)
    monkeypatch.setattr(web.app.state, "gateway_runner", object() if which == "http" else None, raising=False)
    return web.app.router.lifespan_context(web.app)


@pytest.mark.asyncio
@pytest.mark.parametrize("which", ["standalone", "http"])
async def test_a_sender_that_cannot_start_leaves_push_off_and_the_web_server_starts(work, key, monkeypatch, caplog,
                                                                                    which):
    (work.root / "push").write_text("not a folder", encoding="utf-8")
    entered = False
    with caplog.at_level(logging.WARNING, logger="hermes_cli"):
        async with _lifespan(monkeypatch, which):
            entered = True
            assert not [t for t in threading.enumerate() if t.name == "lina-push"]
    assert entered
    pushes = [r for r in caplog.records if r.getMessage().startswith("push:")]
    assert [r.getMessage() for r in pushes] == ["push: off, the sender did not start"]
    assert pushes[0].exc_info and issubclass(pushes[0].exc_info[0], OSError)


@pytest.mark.asyncio
@pytest.mark.parametrize("which", ["standalone", "http"])
async def test_a_stop_that_raises_never_reaches_the_lifespan_teardown(work, monkeypatch, caplog, which):
    def stop():
        raise OSError("lock file gone")
    monkeypatch.setattr(lina_push_watch, "start", lambda: stop)
    with caplog.at_level(logging.WARNING, logger="hermes_cli"):
        async with _lifespan(monkeypatch, which):
            pass
    pushes = [r for r in caplog.records if r.getMessage().startswith("push:")]
    assert [r.getMessage() for r in pushes] == ["push: the sender did not stop cleanly"] and pushes[0].exc_info


def test_a_stop_during_a_slow_send_keeps_the_lock_until_the_thread_ends(work, device, monkeypatch):
    _sender(work, PushService(device)).sweep()  # seeded
    work.ok("wait", "T1", "--decide", "색을 고를까?")
    work.ok("wait", "T2", "--decide", "로그 보관을 늘릴까?")  # two messages owed to the device
    service = PushService(device)
    sending, release = threading.Event(), threading.Event()

    class Slow(Sender):
        def __init__(self, root, vapid):
            super().__init__(root, vapid, self.slow_post)

        def slow_post(self, url, headers, body):
            sending.set()
            assert release.wait(30)
            return service.post(url, headers, body)
    monkeypatch.setattr(lina_push_watch, "Sender", Slow)
    monkeypatch.setattr(lina_push_watch, "STOP_SECONDS", 0.2)
    lock = work.root / "push" / "sender.lock"
    halt = lina_push_watch.start(work.root)
    thread = next(t for t in threading.enumerate() if t.name == "lina-push")
    try:
        assert sending.wait(30)
        halt()  # the web server's shutdown gives up waiting; the send is still in flight
        assert thread.is_alive() and lina_push_watch._hold(lock) is None
    finally:
        release.set()
    thread.join(30)
    assert not thread.is_alive()
    fd = lina_push_watch._hold(lock)
    assert fd is not None
    lina_push_watch._release(fd)
    assert len(device.got) == 1  # the stop was seen before the second message
    state = json.loads((work.root / "push" / "state.json").read_text(encoding="utf-8-sig"))
    assert len(state["devices"][lina_push.sub_id(device.endpoint)]["pending"]) == 1  # owed to the next start


def test_without_a_key_the_sender_does_not_start_and_one_warning_is_logged(work, monkeypatch, caplog):
    monkeypatch.delenv(lina_push.KEY_ENV, raising=False)
    with caplog.at_level(logging.INFO, logger="hermes_cli"):
        lina_push_watch.start(work.root)()
    assert not [t for t in threading.enumerate() if t.name == "lina-push"]
    warnings = [r.getMessage() for r in caplog.records if r.levelno >= logging.WARNING]
    assert warnings == [f"push: off ({lina_push.KEY_ENV} is not set)"]
    assert not (work.root / "push").exists()


def test_the_sender_starts_once_per_root_with_the_key_and_stops(work, key, monkeypatch):
    swept = threading.Event()
    original = Sender.sweep

    def sweep(self):
        original(self)
        swept.set()
    monkeypatch.setattr(Sender, "sweep", sweep)
    halt = lina_push_watch.start(work.root)
    try:
        assert swept.wait(30)
        assert (work.root / "push" / "state.json").is_file()
        assert {p.name: stat.S_IMODE(p.stat().st_mode) for p in (work.root / "push").iterdir()} == {
            "state.json": 0o600, "sender.lock": 0o600}
        lina_push_watch.start(work.root)()  # a second web server on the same root does not send too
        assert len([t for t in threading.enumerate() if t.name == "lina-push"]) == 1
    finally:
        halt()
    assert not [t for t in threading.enumerate() if t.name == "lina-push"]
