"""``lina runners sync``: once a day the runner pins follow their sources, after a trial on the hub (lina#238).

For each runner block whose pin has ``follow`` (omo, codex, claude, dsh, grok, cursor-agent, agy, and any block the
settings add; OCX and the ACP adapters keep LINA's pins):

1. the source names its newest version (``lina_runner_sync_sources``);
2. a version above the hub's pin is installed on the hub beside the current one (``lina_runners._install_version``:
   the installer that owns the binary, its hash, ``--version``) and asked one real request
   (``lina_runner_sync_trial``); when it answers, the hub writes the move into its ``runner-pins.json`` and switches;
3. every node follows the hub's pin the same day through its ``runner-sync`` verb (``lina_runner_sync_node``);
4. the block's model list, route and real-call probe are cared for on the hub and then on every node
   (``lina_runner_sync_tend``, lina#531).

What stops a move: a hold (a failed trial or node install, until ``sync clear``); a major jump (until ``sync clear
--accept <version>``); a source below the pin; a hash that does not match; the hub not signed in to the runner (a
question for a person, not a failure). A node with a run holding a slot, an unreachable node and a node without the
verb are skipped that day and follow on a later one. ``--plan`` only shows what a run would do and writes nothing.

Every run leaves ``runner-sync/YYYYMMDD.json`` and ``runner-sync/last.json``; one short English notice goes through
``worker.notice.command`` when something moved, failed or waits. ``sync status --issues`` prints the lines the hub's
daily check reads.
"""

from __future__ import annotations

import contextlib
import dataclasses
import fcntl  # windows-footgun: ok — the runner sync runs on the POSIX hub only
import json
import os
import re
import shutil
import subprocess
import tempfile
import time
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable

from hermes_cli import lina_ocx, lina_runner_models, lina_runners
from hermes_cli import lina_runner_sync_hub as hub_view
from hermes_cli import lina_runner_sync_sources as sources
from hermes_cli import lina_runner_sync_state as state
from hermes_cli import lina_runner_sync_tend as tending
from hermes_cli import lina_runner_sync_trial as trials
from hermes_cli.lina_runner_sync_node import VERB

LAG_ISSUE_S = 24 * 3600
NODE_TIMEOUT_S = 1800
_SECRET_NAME = re.compile(r"KEY|TOKEN|SECRET|PASSWORD|CREDENTIAL", re.IGNORECASE)


class SyncBusy(RuntimeError):
    pass


@dataclass
class NodeView:
    name: str
    reachable: bool
    why: str = ""
    platform: str | None = None
    verbs: tuple = ()
    rows: dict = field(default_factory=dict)
    running: int = 0
    ocx: dict | None = None
    call: Callable[..., object] | None = None


@dataclass
class Env:
    http: sources.Http = field(default_factory=sources.UrlHttp)
    run: Callable[..., subprocess.CompletedProcess] = subprocess.run
    popen: Callable[..., object] = subprocess.Popen
    fetch: Callable[[str, Path], None] = lina_runners.download
    ocx_logs: Callable[[Path], object] = trials.read_ocx_logs
    ocx_models: Callable[[Path], object] = lina_runner_models.read_ocx_models
    nodes: Callable[[Path], list[NodeView]] | None = None
    sign_in: Callable[[lina_runners.RunnerPin], str] | None = None
    clock: Callable[[], float] = time.time
    notify: Callable[[Path, str], str] | None = None


def scrub(text: str) -> str:
    """No environment secret and nothing the redactor knows survives into output, records or notices."""
    for name, value in os.environ.items():
        if _SECRET_NAME.search(name) and len(value) >= 6:
            text = text.replace(value, "***")
    try:
        from agent.redact import redact_sensitive_text
        return redact_sensitive_text(text, force=True)
    except ImportError:
        return text


def followed(root: Path, only: list[str] | None = None) -> dict[str, lina_runners.RunnerPin]:
    table = {name: pin for name, pin in lina_runners.pins(root=root).items() if pin.follow}
    unknown = [name for name in only or [] if name not in table]
    if unknown:
        raise lina_runners.RunnersError(f"not followed by the sync: {', '.join(unknown)} (followed: {', '.join(table)})")
    return {name: pin for name, pin in table.items() if not only or name in only}



def _contact(dev, worker_root: Path) -> NodeView:
    from hermes_cli import lina_devices
    from hermes_cli.worker import runners, settings
    from hermes_cli.worker.transport import for_device
    transport = for_device(dev, worker_root)
    reply = transport.refresh(timeout=60)
    hello = transport.descriptor or {}
    if reply.code != 0:
        return NodeView(dev.name, False, hello.get("reason") or reply.err.strip()[-160:] or f"exit {reply.code}")
    caps = hello.get("capabilities") or {}
    found = lina_devices.probe(dev, worker_root, runners.default_name(settings.section(worker_root.parent)))
    rows = {row.get("pin") or row.get("name"): row for row in caps.get("runners") or [] if isinstance(row, dict)}
    return NodeView(dev.name, True, platform=caps.get("platform"), verbs=tuple(caps.get("verbs") or ()), rows=rows,
                    running=int(found.get("running") or 0), ocx=caps.get("ocx"),
                    call=lambda verb, data, timeout: transport.call(verb, data, timeout=timeout))


def contact_nodes(root: Path) -> list[NodeView]:
    from hermes_cli.worker import settings
    worker_root = settings.root(root)
    devices = [dev for dev in settings.devices(root)
               if dev.role == "work" and dev.kind != settings.FILE_KIND and not dev.local]
    if not devices:
        return []
    with ThreadPoolExecutor(max_workers=min(8, len(devices))) as pool:
        return list(pool.map(lambda dev: _contact(dev, worker_root), devices))


def _hub_sign_in(runner: lina_runners.RunnerPin, root: Path) -> str:
    return lina_runners.read_sign_in(root, runner)[0]



class Run:
    def __init__(self, root: Path, env: Env, plan: bool):
        self.root, self.env, self.plan = root, env, plan
        self.now = env.clock()
        self.record: dict = {"day": state.day(self.now), "started": self.now, "plan": plan, "runners": {},
                             "nodes": {}, "events": [], "questions": []}
        self.nodes: list[NodeView] = []
        self.hub_questions: list[dict] = []

    def event(self, line: str) -> None:
        self.record["events"].append(scrub(line))

    def needed_platforms(self) -> set[str]:
        wanted = {node.platform for node in self.nodes if node.reachable and node.platform}
        try:
            wanted.add(lina_ocx.platform_key())
        except lina_ocx.OcxError:
            pass
        return wanted

    def hold(self, name: str, node: str | None, version: str, error: str, kind: str) -> None:
        if not self.plan:
            state.add_hold(self.root, name, node, version, scrub(error), kind, now=self.now)
        self.event(f"{name} {'on ' + node if node else 'hub'} {version}: held ({kind}): {scrub(error)}")


    def hub(self, name: str, runner: lina_runners.RunnerPin, rec: dict) -> lina_runners.RunnerPin:
        try:
            newest = sources.newest(runner.follow, runner.package, self.env.http)
        except sources.SourceError as exc:
            rec["hub"] = {"state": "source error", "reason": scrub(str(exc))}
            return runner
        rec["source"] = newest
        order = state.compare(newest, runner.version)
        if order == 0:
            rec["hub"] = {"state": "same"}
            return self.top_up(name, runner, rec)
        if order < 0:
            rec["hub"] = {"state": "source behind", "reason": f"source names {newest}, below the pin {runner.version}"}
            self.event(f"{name}: the source went back to {newest} (pin {runner.version}); not moved")
            return runner
        if state.major_jump(newest, runner.version) and state.accepted(self.root, name) != newest:
            rec["hub"] = {"state": "major waits", "reason": f"{runner.version} -> {newest}"}
            if not state.held(self.root, name):
                self.hold(name, None, newest, f"major version {runner.version} -> {newest} waits for a person", "major")
            return runner
        if self.plan:
            rec["hub"] = {"state": "would move", "to": newest}
            return dataclasses.replace(runner, version=newest)
        return self.move(name, runner, newest, rec)

    def _resolve(self, runner: lina_runners.RunnerPin, version: str, platforms: set[str], work: Path):
        return sources.resolve(runner.follow, runner.package, version, platforms, self.env.http, work)

    def top_up(self, name: str, runner: lina_runners.RunnerPin, rec: dict) -> lina_runners.RunnerPin:
        """Same version, but a node uses a platform the moved pin has no artifact for yet: add it."""
        missing = self.needed_platforms() - set(runner.artifacts)
        if runner.installer == "npm" or not missing or self.plan or state.pin_from(self.root, name) != "sync":
            return runner
        with tempfile.TemporaryDirectory(prefix="lina-runner-sync-") as work:
            try:
                found = self._resolve(runner, runner.version, missing, Path(work))
            except sources.SourceError as exc:
                rec["hub"]["top_up"] = scrub(str(exc))
                return runner
        artifacts = {**runner.artifacts, **found.artifacts}
        state.write_overlay(self.root, name, {"version": runner.version, "artifacts": artifacts}, why="top up",
                            now=self.now)
        rec["hub"]["top_up"] = sorted(found.artifacts)
        return dataclasses.replace(runner, artifacts=artifacts)

    def move(self, name: str, runner: lina_runners.RunnerPin, newest: str, rec: dict) -> lina_runners.RunnerPin:
        signed = (self.env.sign_in or (lambda pin: _hub_sign_in(pin, self.root)))(runner)
        if signed == "signed_out":
            rec["hub"] = {"state": "hub sign-in needed", "to": newest}
            self.hub_questions.append({"name": name, "sign_in": "signed_out",
                                       "sign_in_by": lina_runners.sign_in_kind(runner)})
            self.event(f"{name} hub {newest}: not moved, the hub is not signed in")
            return runner
        if self.hub_busy(name, newest, rec):
            return runner
        previous = lina_runners.current_version(self.root, runner)
        if previous is None:
            rec["hub"] = {"state": hub_view.PREPARE, "to": newest,
                          "reason": f"`lina runners install {name}` on the hub first"}
            self.event(hub_view.prepare_event(name, newest))
            return runner
        with tempfile.TemporaryDirectory(prefix="lina-runner-sync-") as work:
            try:
                found = self._resolve(runner, newest, self.needed_platforms(), Path(work))
            except sources.SourceError as exc:
                rec["hub"] = {"state": "not moved", "to": newest, "reason": scrub(str(exc))}
                self.event(f"{name} hub {newest}: not moved: {scrub(str(exc))}")
                return runner
            moved = dataclasses.replace(runner, version=newest, integrity=found.integrity or runner.integrity,
                                        artifacts=found.artifacts or runner.artifacts)
            ok, why = self.try_on_hub(moved, found.downloads)
        if not ok:
            rec["hub"] = {"state": "failed", "to": newest, "reason": scrub(why)}
            self.hold(name, "hub", newest, why, "trial")
            return runner
        if self.hub_busy(name, newest, rec):
            self.discard(moved)
            return runner
        return self.switch(name, runner, moved, previous, rec)

    def hub_busy(self, name: str, newest: str, rec: dict) -> bool:
        """The hub is a node too: while a run holds one of its slots it neither installs nor switches today, and
        nothing is held; tomorrow's run tries again."""
        from hermes_cli.worker import settings, slots
        held = slots.held(settings.root(self.root))
        if held:
            rec["hub"] = {"state": "hub busy", "to": newest, "reason": f"{held} run(s) hold a hub slot; next day"}
            self.event(f"{name} hub {newest}: not moved, {held} run(s) on the hub")
        return bool(held)

    def try_on_hub(self, moved: lina_runners.RunnerPin, downloads: dict) -> tuple[bool, str]:
        def fetch(url: str, dest: Path) -> None:
            if url in downloads:
                shutil.copyfile(downloads[url], dest)
            else:
                self.env.fetch(url, dest)
        try:
            lina_runners._install_version(self.root, moved, moved.version, moved.integrity, fetch, self.env.run)
        except lina_runners.OcxError as exc:
            return False, f"install: {exc}"
        ok, why = trials.trial(self.root, moved, moved.version, run=self.env.run, popen=self.env.popen,
                               ocx_logs=self.env.ocx_logs, clock=self.env.clock)
        if not ok:
            self.discard(moved)
        return ok, why if ok else f"trial: {why}"

    def discard(self, moved: lina_runners.RunnerPin) -> None:
        """A version that failed its trial is not kept: a later retry must download and check it again."""
        if moved.version != lina_runners.current_version(self.root, moved):
            shutil.rmtree(lina_runners.vendor_dir(self.root, moved) / moved.version, ignore_errors=True)

    def switch(self, name: str, runner, moved, previous: str, rec: dict) -> lina_runners.RunnerPin:
        entry = {"version": moved.version, **({"integrity": moved.integrity} if moved.installer == "npm"
                                               else {"artifacts": moved.artifacts})}
        before = state.overlay(self.root).get(name)
        state.write_overlay(self.root, name, entry, why="hub trial passed", now=self.now)
        said: list[str] = []
        if lina_runners._switch(self.root, moved, previous, moved.version, self.env.run, said.append):
            state.write_overlay(self.root, name, before, why="hub switch failed", now=self.now)
            rec["hub"] = {"state": "failed", "to": moved.version, "reason": scrub(" ".join(said))}
            self.hold(name, "hub", moved.version, " ".join(said), "switch")
            return runner
        rec["hub"] = {"state": "moved", "from": previous, "to": moved.version}
        self.event(f"{name} hub {previous} -> {moved.version}: moved")
        return moved


    def follow(self, name: str, pin: lina_runners.RunnerPin, rec: dict) -> None:
        results = rec.setdefault("nodes", {})
        entry = {"version": pin.version, **({"integrity": pin.integrity} if pin.installer == "npm"
                                             else {"artifacts": pin.artifacts})}
        for node in self.nodes:
            row = node.rows.get(name) if node.reachable else None
            if node.reachable and row is None:
                continue
            result = self.node_step(name, pin, node, row, entry)
            results[node.name] = result
            if not self.plan and node.reachable:
                version = pin.version if result["state"] in ("switched", "same") else row.get("installed")
                state.update_lag(self.root, node.name, name, version, pin.version, self.now)

    def node_step(self, name: str, pin, node: NodeView, row: dict | None, entry: dict) -> dict:
        if not node.reachable:
            return {"state": "unreachable", "reason": scrub(node.why)}
        if state.held(self.root, name, node.name):
            return {"state": "held"}
        if VERB not in node.verbs:
            return {"state": "update", "reason": f"the node has no `{VERB}` verb yet"}
        if row.get("installed") is None:
            return {"state": "prepare", "reason": "not installed on the node"}
        if row.get("installed") == pin.version and row.get("pinned") == pin.version:
            return {"state": "same"}
        if self.plan:
            return {"state": "would follow", "from": row.get("installed"), "to": pin.version}
        if node.running:
            return {"state": "busy", "reason": f"{node.running} run(s) on the node; next day"}
        reply = node.call([VERB], json.dumps({"runners": {name: entry}}).encode(), NODE_TIMEOUT_S)
        got = (reply.json().get("results") or {}).get(name) or \
            {"state": "failed", "reason": reply.json().get("reason") or (reply.err or "").strip()[-200:]
             or f"exit {reply.code}"}
        got = {**got, "reason": scrub(str(got.get("reason") or ""))} if got.get("reason") else got
        if got["state"] == "failed":
            self.hold(name, node.name, pin.version, got.get("reason") or "install failed", "node")
        elif got["state"] == "switched":
            self.event(f"{name} {node.name} {row.get('installed')} -> {pin.version}: followed")
        return got

    def questions(self) -> list[dict]:
        from hermes_cli.lina_devices import sign_in_questions
        rows = [{"name": node.name, "runners": list(node.rows.values())} for node in self.nodes if node.reachable]
        if self.hub_questions:
            rows.append({"name": hub_view.hub_name(self.root), "runners": self.hub_questions})
        return sign_in_questions(rows)



@contextlib.contextmanager
def run_lock(root: Path):
    path = state.sync_dir(root) / "lock"
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a") as handle:
        try:
            fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)  # windows-footgun: ok — POSIX hub only
        except BlockingIOError as exc:
            raise SyncBusy("another `lina runners sync` is running") from exc
        try:
            yield
        finally:
            fcntl.flock(handle, fcntl.LOCK_UN)  # windows-footgun: ok — POSIX hub only


def _summarise_nodes(nodes: list[NodeView]) -> dict:
    return {node.name: {"reachable": node.reachable, "why": scrub(node.why), "platform": node.platform,
                        "ocx": node.ocx, "running": node.running,
                        "runners": {name: {"installed": row.get("installed"), "pinned": row.get("pinned"),
                                           "sign_in": row.get("sign_in")} for name, row in node.rows.items()}}
            for node in nodes}


def execute(root: Path, env: Env, *, plan: bool, only: list[str] | None) -> dict:
    run = Run(root, env, plan)
    selected = followed(root, only)
    run.nodes = (env.nodes or contact_nodes)(root)
    run.record["nodes"] = _summarise_nodes(run.nodes)
    for name, runner in selected.items():
        rec = run.record["runners"].setdefault(name, {"pin": runner.version})
        if state.held(root, name) or state.held(root, name, "hub"):
            rec["hub"] = {"state": "held"}
            continue
        pin = run.hub(name, runner, rec)
        if rec["hub"]["state"] in ("major waits", "hub busy"):
            continue
        run.follow(name, pin, rec)
        if not state.held(root, name, "hub"):
            tending.tend(run, name, pin, rec)
    run.record["questions"] = run.questions()
    run.record["finished"] = env.clock()
    return run.record


def _notice_text(record: dict) -> str:
    return "\n".join([f"runner sync {record['day']}:"] + [f"- {line}" for line in record["events"]]) + "\n"


def notify(root: Path, text: str) -> str:
    """The end notice through ``worker.notice.command`` to ``worker.runner_sync.notify`` (default ``default``)."""
    from hermes_cli.worker import notice, settings
    worker = settings.section(root)
    command = (worker.get("notice") or {}).get("command") if isinstance(worker.get("notice"), dict) else None
    sync = worker.get("runner_sync") if isinstance(worker.get("runner_sync"), dict) else {}
    job = {"id": "runner-sync", "bot": str(sync.get("notify") or "default")}
    return notice.deliver(job, text, "runner-sync", settings.hub_dir(settings.root(root)) / "notices", command).how


def sync(root: Path | None = None, *, plan: bool = False, only: list[str] | None = None, as_json: bool = False,
         env: Env | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    env = env or Env()
    if plan:
        record = execute(root, env, plan=True, only=only)
        _show(record, as_json, out)
        return 0
    try:
        with run_lock(root):
            started = env.clock()
            state.write_json(state.last_path(root), {**hub_view.carried(state.last(root), started), "started": started,
                                                     "finished": None, "rc": None})
            rc, retry = 1, {}
            try:
                record = execute(root, env, plan=False, only=only)
                rc = 1 if any(e for e in record["events"] if ": held (" in e) else 0
                retry = hub_view.retry_mark(record, state.last(root))
                path = state.record_path(root, record["started"])
                state.write_json(path, hub_view.day_record(path, record) if only else record)
                if record["events"]:
                    record["notice"] = (env.notify or notify)(root, _notice_text(record))
                _show(record, as_json, out)
            finally:
                last = state.last(root)
                state.write_json(state.last_path(root), {**last, "finished": env.clock(), "rc": rc, **retry})
    except SyncBusy as exc:
        out(str(exc))
        return 1
    return rc


def _show(record: dict, as_json: bool, out) -> None:
    if as_json:
        out(json.dumps(record, indent=2, ensure_ascii=False))
        return
    for name, rec in record["runners"].items():
        hub = rec.get("hub") or {}
        out(f"{name:<13} pin {rec['pin']} · source {rec.get('source') or '-'} · hub {hub.get('state')}"
            + (f" ({hub['reason']})" if hub.get("reason") else ""))
        for node, result in (rec.get("nodes") or {}).items():
            out(f"  {node:<14} {result['state']}" + (f" ({result['reason']})" if result.get("reason") else ""))
    for ask in record["questions"]:
        out(ask["question"])



def _last_record(root: Path) -> dict:
    found = sorted(state.sync_dir(root).glob("[0-9]" * 8 + ".json"))
    return state.read_json(found[-1], {}) if found else {}


def issue_lines(root: Path, now: float | None = None) -> tuple[list[str], list[str]]:
    """(issues, notes) as one line each: what the hub's daily check reads."""
    now = time.time() if now is None else now
    issues, notes = [], []
    for hold in state.holds(root)["holds"]:
        where = f" on {hold['node']}" if hold.get("node") else ""
        issues.append(f"- hold: {hold['runner']}{where} {hold.get('version')} ({hold.get('kind')}): {hold.get('error')}")
    for key, entry in sorted(state.lag(root).items()):
        node, _, runner = key.partition("/")
        since = entry.get("since")
        age = now - (float(since) if isinstance(since, (int, float)) else now)
        line = (f"- lag: {runner} on {node} {entry.get('node_version') or 'none'}, hub {entry.get('hub_version')} "
                f"for {age / 3600:.0f}h")
        (issues if age > LAG_ISSUE_S else notes).append(line)
    record = _last_record(root)
    for node, view in sorted((record.get("nodes") or {}).items()):
        if not view.get("reachable"):
            notes.append(f"- unreachable: {node}")
            continue
        ocx = view.get("ocx")
        if isinstance(ocx, dict) and ocx.get("health") != 200:
            issues.append(f"- ocx: {node} health {ocx.get('health')}" + (" (external)" if ocx.get("external") else ""))
    issues += hub_view.prepare_issues(record)
    for ask in record.get("questions") or []:
        issues.append(f"- sign-in: {ask['question']}")
    if state.last(root).get("error"):
        issues.append(f"- daily: could not start: {state.last(root)['error']}")
    return [scrub(line) for line in issues], [scrub(line) for line in notes]


def status(root: Path | None = None, *, issues_only: bool = False, as_json: bool = False, out=print) -> int:
    root = root or lina_ocx.lina_root()
    issues, notes = issue_lines(root)
    if issues_only:
        for line in issues or ["- none"]:
            out(line)
        return 0
    repo = lina_runners.pins(root=root, moved=False)
    table = [{"runner": name, "repo": repo[name].version, "hub": pin.version,
              "from": state.pin_from(root, name)} for name, pin in followed(root).items()]
    data = {"pins": table, "holds": state.holds(root), "last": state.last(root), "issues": issues, "notes": notes}
    if as_json:
        out(json.dumps(data, indent=2, ensure_ascii=False))
        return 0
    for row in table:
        out(f"{row['runner']:<13} repo {row['repo']:<22} hub {row['hub']:<22} ({row['from']})")
    last = data["last"]
    out(f"last run: started {_when(last.get('started'))} · finished {_when(last.get('finished'))} · rc {last.get('rc')}")
    out("issues:")
    for line in issues or ["- none"]:
        out(line)
    out("notes:")
    for line in notes or ["- none"]:
        out(line)
    return 0


def _when(stamp) -> str:
    return time.strftime("%Y-%m-%d %H:%M", time.localtime(stamp)) if isinstance(stamp, (int, float)) else "-"


def clear(runner: str, root: Path | None = None, *, node: str | None = None, accept: str | None = None,
          out=print) -> int:
    root = root or lina_ocx.lina_root()
    followed(root, [runner])
    gone = state.clear(root, runner, node, accept)
    out(f"{runner}: cleared {len(gone)} hold(s)" + (f"; {accept} accepted" if accept else ""))
    return 0
