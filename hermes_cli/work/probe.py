"""``lina work probe``: read-only checks a work item's ``--check`` runs. Exit 0 = the condition holds, 1 = not yet,
2 = cannot tell; one line of evidence on stdout. Probes only read: ``gh api`` GETs, files, the session queue
(``work.session_queue``), the worker's job files, the work record and the configured Todoist probe."""

from __future__ import annotations

import json
import os
import re
import subprocess
import time
from pathlib import Path

from hermes_cli.work.locale import t

TIMEOUT = 25


class Verdict(SystemExit):
    """The probe's answer: exit *code* after printing *line*."""

    def __init__(self, code: int, line: str):
        print(line)
        super().__init__(code)


def _run(argv: list[str], timeout: float | None = None) -> tuple[int, str]:
    p = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", timeout=timeout or TIMEOUT,
                       stdin=subprocess.DEVNULL, check=False)
    return p.returncode, p.stdout


def _gh(path: str, spec: str) -> dict:
    rc, out = _run(["gh", "api", path])
    try:
        data = json.loads(out) if rc == 0 else None
    except ValueError:
        data = None
    if not isinstance(data, dict):
        raise Verdict(2, t("GitHub 못 읽음: {spec}", spec=spec))
    return data


def _repo(spec: str) -> str:
    from hermes_cli.work.links import gh_repo
    owner, repo = gh_repo(spec)
    if not owner:
        raise Verdict(2, t("저장소 주인을 모름: {spec} (work.github)", spec=spec))
    return f"{owner}/{repo}"


def _split(arg: str, sep: str) -> tuple[str, str]:
    head, _, tail = arg.rpartition(sep)
    if not head or not tail.isdigit():
        raise Verdict(2, t("형식이 틀림: {arg}", arg=arg))
    return head, tail


def gh_closed(args: list[str]) -> None:
    """every issue/PR closed (a PR: merged)"""
    good, bad = [], []
    for a in args:
        spec, n = _split(a, "#")
        d = _gh(f"repos/{_repo(spec)}/issues/{n}", a)
        pr = d.get("pull_request")
        (good if d.get("state") == "closed" and (not pr or pr.get("merged_at")) else bad).append(a)
    line = (t("닫힘 {ids}", ids=" ".join(good)) if good else "") + (t(" / 열림 {ids}", ids=" ".join(bad)) if bad else "")
    raise Verdict(1 if bad else 0, line.strip(" /"))


def ms_closed(args: list[str]) -> None:
    """the milestone has 0 open issues (it does not say the goal is met)"""
    spec, n = _split(args[0], "/")
    d = _gh(f"repos/{_repo(spec)}/milestones/{n}", args[0])
    opened, closed = int(d.get("open_issues") or 0), int(d.get("closed_issues") or 0)
    raise Verdict(0 if opened == 0 and closed else 1,
                  t("{title}: {closed}/{total} 닫힘", title=d.get("title"), closed=closed, total=opened + closed))


def file_(args: list[str]) -> None:
    """the file exists (and contains the regex)"""
    p = Path(os.path.expanduser(args[0]))
    if not p.exists():
        raise Verdict(1, t("없음: {name}", name=p.name))
    if len(args) > 1:
        try:
            found = re.search(args[1], p.read_text(encoding="utf-8-sig", errors="replace")) is not None
        except (re.error, OSError) as e:
            raise Verdict(2, t("못 읽음: {name} ({e})", name=p.name, e=e)) from None
        raise Verdict(0 if found else 1, t("{name}: {state} ({pattern})", name=p.name,
                                           state=t("있음") if found else t("내용 없음"), pattern=args[1]))
    raise Verdict(0, t("있음: {name}", name=p.name))


def fresh(args: list[str]) -> None:
    """the path was modified within <hours>"""
    p = Path(os.path.expanduser(args[0]))
    try:
        hours = float(args[1])
    except (IndexError, ValueError):
        raise Verdict(2, t("시간이 필요하다: fresh <경로> <시간>")) from None
    if not p.exists():
        raise Verdict(1, t("없음: {name}", name=p.name))
    age = (time.time() - p.stat().st_mtime) / 3600
    raise Verdict(0 if age <= hours else 1, t("{name} {age}시간 전 수정", name=p.name, age=f"{age:.1f}"))


def session_ok(args: list[str]) -> None:
    """the topic session's last run exited 0, or it is running (``work.session_queue``)"""
    from hermes_cli.work.sources import queue_dir
    profile, _, name = args[0].partition(":")
    root = queue_dir()
    if not root or not name:
        raise Verdict(2, t("세션 큐가 없다 (work.session_queue)") if not root else t("형식이 틀림: {arg}", arg=args[0]))
    last = None
    for bucket in ("running", "done"):
        for f in sorted((root / bucket).glob("*.json")):
            try:
                x = json.loads(f.read_text(encoding="utf-8-sig"))
            except (OSError, ValueError):
                continue
            if not isinstance(x, dict) or x.get("profile") != profile or x.get("name") != name:
                continue
            if bucket == "running":
                raise Verdict(0, t("{name} 도는 중", name=name))
            if not last or (x.get("ended") or 0) > (last.get("ended") or 0):
                last = x
    if not last:
        raise Verdict(1, t("{name} 기록 없음", name=name))
    ok = last.get("state") == "exited" and last.get("rc") == 0
    raise Verdict(0 if ok else 1, t("{name} 마지막 {state} rc={rc}", name=name, state=last.get("state"), rc=last.get("rc")))


def todoist_done(args: list[str]) -> None:
    """every Todoist task is completed (the read-only probe in ``work.probes.todoist``: exit 0 done, 1 not)"""
    from hermes_cli.work.env import current
    probes = current().section("work").get("probes")
    argv = probes.get("todoist") if isinstance(probes, dict) else None
    if not isinstance(argv, list) or not argv:
        raise Verdict(2, t("Todoist 검사가 없다 (work.probes.todoist)"))
    good, bad = [], []
    for task in args:
        task = task.split(":", 1)[1] if task.startswith("todoist:") else task
        rc, _ = _run([str(a) for a in argv] + [task], 45)
        if rc not in (0, 1):
            raise Verdict(2, t("Todoist 못 읽음: {id}", id=task))
        (good if rc == 0 else bad).append(task)
    line = (t("끝남 {ids}", ids=" ".join(good)) if good else "") + (t(" / 안 끝남 {ids}", ids=" ".join(bad)) if bad else "")
    raise Verdict(1 if bad else 0, line.strip(" /"))


def worker_running(args: list[str]) -> None:
    """each worker device has at least one job in state running (the worker hub's job files)"""
    from hermes_cli.work.run import _worker_root
    from hermes_cli.worker.hub_store import Store
    try:
        jobs = Store(_worker_root()).all()
    except (OSError, ValueError) as e:
        raise Verdict(2, t("작업 기록 못 읽음: {e}", e=e)) from None
    count: dict[str, int] = {}
    for j in jobs:
        if isinstance(j, dict) and j.get("state") == "running":
            count[str(j.get("host"))] = count.get(str(j.get("host")), 0) + 1
    raise Verdict(0 if all(count.get(h) for h in args) else 1, " ".join(f"{h}={count.get(h, 0)}" for h in args))


def agents_goals(args: list[str]) -> None:
    """at least <n> agents wrote a goal into the record themselves (not seeded for them by another agent)"""
    from hermes_cli.work import ledger
    from hermes_cli.work.cmd_read import first_writers
    from hermes_cli.work.people import agents, name
    try:
        need = int(args[0])
    except ValueError:
        raise Verdict(2, t("형식이 틀림: {arg}", arg=args[0])) from None
    recs, order = ledger.read_all()
    writers = first_writers()
    known = dict(agents())
    who = sorted({name(writers.get(i)) for i in order
                  if recs[i].get("kind") == "목표" and writers.get(i) in known and writers.get(i) == recs[i].get("by")})
    raise Verdict(0 if len(who) >= need else 1, t("목표를 직접 넣은 에이전트 {n}: {names}", n=len(who), names=" ".join(who)))


# name -> (handler, least args, usage)
PROBES = {
    "gh-closed": (gh_closed, 1, "<repo>#<n> [...]"),
    "ms-closed": (ms_closed, 1, "<repo>/<n>"),
    "file": (file_, 1, "<path> [regex]"),
    "fresh": (fresh, 2, "<path> <hours>"),
    "session-ok": (session_ok, 1, "<profile>:<topic>"),
    "todoist-done": (todoist_done, 1, "<task id> [...]"),
    "worker-running": (worker_running, 1, "<device> [...]"),
    "agents-goals": (agents_goals, 1, "<n>"),
}


def usage() -> str:
    return "\n".join(f"  lina work probe {k} {u}  — {(f.__doc__ or '').strip()}" for k, (f, _, u) in PROBES.items())


def cmd_probe(a) -> None:
    handler, least, _ = PROBES[a.probe]
    if len(a.args) < least:
        raise Verdict(2, usage())
    try:
        handler(a.args)
    except subprocess.TimeoutExpired:
        raise Verdict(2, t("시간 초과")) from None
    except OSError as e:
        raise Verdict(2, t("실행 실패: {e}", e=e)) from None
