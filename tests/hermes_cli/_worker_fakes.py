"""Fakes for ``lina worker`` (lina#35): a worker root with a local device, a fake omo, a fake omo engine, a fake CLI.

* The fake omo is a Python program called exactly like the pinned ``bin/omo``. It answers ``omo daemon run --json``
  and ``omo host session open|prompt|steer|abort|state|read|wait … --json`` with the JSON shapes the omo runner reads
  (one line on stdout), from one session kept in ``<fake>/state.json``. The session's ``phase`` picks the lifecycle
  ``state`` answers with: ``question`` (``waiting_human`` with a pending question), ``running``, ``hang`` (idle with
  nothing to wait on), ``done`` / ``nonotes`` / ``empty`` (sealed ``done``; the agent's files are written into the
  session's cwd on the first read), ``answered`` (the question was answered: then ``done``), ``cancelled``.
* The fake engine is ``dist/modes/rpc/rpc-client.js`` + ``host-session-ref.js``: the two modules ``omo_answer.mjs``
  and ``omo_open.mjs`` import from the pinned engine. They run on the real Node and act on the same state file.
* The fake CLI reads its prompt on stdin and acts on the ``SCENARIO:<name>`` it finds there.
"""

from __future__ import annotations

import json
import sys
import textwrap
import threading
from pathlib import Path

from hermes_cli.worker import device
from tests.waiting import wait_until

REPORT_WITH_NOTES = "# report\n\n## 만든 것\n- out/ok.txt\n\n## 다음에 알아야 할 것\n- the fake agent needs no network\n"
REPORT_WITHOUT_NOTES = "# report\n\n## 만든 것\n- out/ok.txt\n"

FAKE_OMO = '''#!{python}
import json, os, sys
fake = os.environ["FAKE_OMO_DIR"]
state_file = os.path.join(fake, "state.json")
with open(os.path.join(fake, "calls.jsonl"), "a") as fh:
    fh.write(json.dumps(sys.argv[1:]) + "\\n")
def load():
    return json.load(open(state_file)) if os.path.exists(state_file) else {{}}
def save(s):
    temporary = state_file + "." + str(os.getpid()) + ".tmp"
    with open(temporary, "w") as fh:
        json.dump(s, fh)
    os.replace(temporary, state_file)
def say(o):
    print(json.dumps(o))
    sys.exit(0)
def materialize(s):
    if s.get("materialized") or s["phase"] not in ("done", "nonotes", "answered"):
        return False
    os.makedirs(os.path.join(s["cwd"], "out"), exist_ok=True)
    open(os.path.join(s["cwd"], "out", "ok.txt"), "w").write("ok\\n")
    open(os.path.join(s["cwd"], "report.md"), "w").write({with_notes!r} if s["phase"] != "nonotes" else {without!r})
    s["materialized"] = True
    return True
LIFECYCLES = {{
    "question": {{"state": "waiting_human", "since": "2026-11-02T10:00:00.000Z", "seq": 3,
                  "waitingOn": {{"human": [{{"kind": "question", "requestId": "req-1"}}]}}}},
    "running": {{"state": "running", "seq": 2, "waitingOn": {{}}}},
    "hang": {{"state": "idle", "seq": 4, "waitingOn": {{}}}},
    "cancelled": {{"state": "cancelled", "seq": 9, "waitingOn": {{}}, "failure": {{"reason": "aborted"}}}},
}}
args = sys.argv[1:]
if args[:2] == ["daemon", "run"]:
    say({{"action": "daemon", "lifecycle": "ready", "pid": 4242}})
verb, rest = args[2], args[3:]
s = load()
if verb == "open":
    cwd = rest[rest.index("--cwd") + 1]
    scenario = json.load(open(os.path.join(fake, "scenario.json")))
    import datetime
    s = {{"phase": scenario["first"], "cwd": cwd, "prompts": [open(rest[rest.index("--prompt") + 1][1:]).read()],
          "opened_at": datetime.datetime.now(datetime.timezone.utc).isoformat()}}
    save(s)
    say({{"action": "open", "sessionId": "host-1", "durableSessionId": "ses-fake-1", "sessionPath": "/fake/ses.jsonl",
          "cwd": cwd, "name": rest[rest.index("--name") + 1], "reused": False}})
if verb == "prompt":
    if s.get("resolution", "live") != "live":
        print(json.dumps({{"action": "error", "reason": "session not live"}}))
        sys.exit(1)
    text = open(rest[1][1:]).read()
    s.setdefault("prompts", []).append(text)
    scenario = json.load(open(os.path.join(fake, "scenario.json")))
    s["phase"] = "answered" if "Answer to the earlier question" in text else scenario.get("follow_up", "done")
    s["materialized"] = False
    save(s)
    say({{"action": "prompt", "disposition": "accepted"}})
if verb == "steer":
    s.setdefault("steers", []).append(open(rest[1][1:]).read())
    save(s)
    say({{"action": "steer", "disposition": "queued"}})
if verb == "abort":
    s["phase"] = "cancelled" if s.get("phase") != "question" else "hang"
    save(s)
    say({{"action": "abort"}})
if verb == "wait":
    say({{"action": "wait", "outcome": "idle"}})
if verb == "read":
    if "--messages" in rest:
        say({{"action": "read", "messages": [{{"role": "user", "content": [{{"type": "text", "text": "brief"}}]}},
             {{"role": "assistant", "content": [{{"type": "text", "text": "fake final answer"}}]}}]}})
    entries = [] if "--since" in rest else [{{"id": "e-1", "type": "message", "timestamp": s.get("opened_at"),
               "message": {{"role": "assistant", "content": [{{"type": "text", "text": "working on it"}}]}}}}]
    say({{"action": "read", "entries": entries, "nextSince": "cursor-1"}})
if verb == "state":
    if materialize(s):
        save(s)
    lifecycle = LIFECYCLES.get(s["phase"], {{"state": "done", "seq": 7, "waitingOn": {{}}, "completionId": "c-1"}})
    say({{"action": "state", "state": {{"lifecycle": lifecycle, "isStreaming": s["phase"] == "running", "isBashRunning": False,
          "pendingMessageCount": 0, "pendingQuestions": [{{"id": "q-1", "requestId": "req-1"}}]
          if s["phase"] == "question" else [], "pendingDialogs": []}}}})
say({{"action": "error", "reason": "usage", "detail": " ".join(args)}})
'''

RPC_CLIENT_JS = """
import { readFileSync, writeFileSync, appendFileSync, renameSync } from "node:fs";
import { join } from "node:path";
const fake = process.env.FAKE_OMO_DIR;
const stateFile = join(fake, "state.json");
const load = () => JSON.parse(readFileSync(stateFile, "utf8"));
const save = (s) => {
  const temporary = `${stateFile}.${process.pid}.tmp`;
  writeFileSync(temporary, JSON.stringify(s));
  renameSync(temporary, stateFile);
};
export class RpcClient {
  constructor(options) { appendFileSync(join(fake, "rpc.jsonl"), JSON.stringify({ new: options }) + "\\n"); }
  async start() {}
  async stop() {}
  async openSession(options) {
    appendFileSync(join(fake, "rpc.jsonl"), JSON.stringify({ openSession: options }) + "\\n");
    if (options.sessionPath) {
      const s = load();
      if (options.sessionPath !== "/fake/ses.jsonl" || options.cwd !== s.cwd) throw new Error("wrong session");
      s.resolution = "live";
      s.attached = options;
      writeFileSync(stateFile, JSON.stringify(s));
      return {};
    }
    const scenario = JSON.parse(readFileSync(join(fake, "scenario.json"), "utf8"));
    save({ phase: scenario.first, cwd: options.cwd, prompts: [], opened: options,
           opened_at: new Date().toISOString() });
    return { sessionId: "host-1", state: { sessionFile: "/fake/ses.jsonl", sessionId: "ses-fake-1", cwd: options.cwd,
             model: { provider: options.provider, id: options.modelId }, thinkingLevel: options.thinkingLevel } };
  }
  async setSessionName(name) { const s = load(); s.name = name; save(s); }
  async prompt(text) { const s = load(); s.prompts.push(text); save(s); return "accepted"; }
  async getState() {
    const s = load();
    const open = s.phase === "question";
    return { pendingQuestions: open ? [{ id: "q-1", requestId: "req-1" }] : [], pendingDialogs: [], isStreaming: false,
             lifecycle: { state: open ? "waiting_human" : "running", waitingOn: { human: [] } } };
  }
  async sendExtensionUIResponse(frame) {
    appendFileSync(join(fake, "rpc.jsonl"), JSON.stringify({ response: frame }) + "\\n");
    const s = load();
    if (process.env.FAKE_ANSWER_REFUSED !== "1") s.phase = "answered";
    s.materialized = false;
    save(s);
    if (process.env.FAKE_ANSWER_REFUSED === "1") { s.phase = "question"; save(s); }
  }
}
"""

SESSION_REF_JS = """
import { readFileSync } from "node:fs";
import { join } from "node:path";
export async function resolveSessionRef(client, ref) {
  const s = JSON.parse(readFileSync(join(process.env.FAKE_OMO_DIR, "state.json"), "utf8"));
  if (s.resolution === "parked") return { kind: "parked", sessionPath: "/fake/ses.jsonl", cwd: s.cwd,
                                        durableSessionId: "ses-fake-1" };
  if (s.resolution === "unknown") return { kind: "unknown" };
  if (s.resolution === "ambiguous") return { kind: "ambiguous", candidates: ["host-1", "host-2"] };
  return ref === "ses-fake-1" ? { kind: "live", row: { sessionPath: "/fake/ses.jsonl", cwd: s.cwd } } : { kind: "unknown" };
}
"""

FAKE_CLI = '''import json, os, re, sys, time
text = sys.stdin.read()
with open(os.environ["FAKE_CLI_LOG"], "a") as fh:
    fh.write(json.dumps({{"argv": sys.argv[1:], "prompt": text[:120]}}) + "\\n")
found = re.search(r"SCENARIO:(\\w+)", text)
mode = "done" if "Answer to your question" in text else (found.group(1) if found else "done")
if mode == "ask":
    open("question.md", "w").write("Which colour should ok.txt say?\\n")
elif mode in ("done", "nonotes"):
    os.makedirs("out", exist_ok=True)
    open(os.path.join("out", "ok.txt"), "w").write("ok\\n")
    open("report.md", "w").write({with_notes!r} if mode == "done" else {without!r})
    print("fake cli final answer")
elif mode == "fail":
    print("401 not authenticated", file=sys.stderr)
    sys.exit(3)
elif mode == "hang":
    time.sleep(120)
'''


def write_config(base: Path, runners_yaml: str, extra: str = "", stall: float = 30) -> None:
    base.mkdir(parents=True, exist_ok=True)
    (base / "config.yaml").write_text(textwrap.dedent(f"""\
        devices:
          workers:
            here: {{host: local, cap: 2}}
        worker:
          poll_s: 0.05
          stall_grace_s: {stall}
          answered_grace_s: 0.2
          min_free_gb: 0
          runners:
        {textwrap.indent(textwrap.dedent(runners_yaml), "    ")}
        """) + extra, encoding="utf-8")


def install_fake_omo(fake: Path, node: str) -> dict:
    """The fake omo, the fake engine and a runner spec pointing the omo runner at them."""
    fake.mkdir(parents=True, exist_ok=True)
    omo = fake / "omo"
    omo.write_text(FAKE_OMO.format(python=sys.executable, with_notes=REPORT_WITH_NOTES, without=REPORT_WITHOUT_NOTES),
                   encoding="utf-8")
    omo.chmod(0o755)
    rpc = fake / "engine" / "dist" / "modes" / "rpc"
    rpc.mkdir(parents=True)
    (rpc / "rpc-client.js").write_text(RPC_CLIENT_JS, encoding="utf-8")
    (rpc / "host-session-ref.js").write_text(SESSION_REF_JS, encoding="utf-8")
    (fake / "engine" / "package.json").write_text('{"type": "module"}', encoding="utf-8")
    return {"type": "omo", "bin": str(omo), "node": node, "engine": str(fake / "engine")}


def install_fake_cli(fake: Path) -> list[str]:
    fake.mkdir(parents=True, exist_ok=True)
    cli = fake / "fake_cli.py"
    cli.write_text(FAKE_CLI.format(with_notes=REPORT_WITH_NOTES, without=REPORT_WITHOUT_NOTES), encoding="utf-8")
    return [sys.executable, str(cli)]


def until(predicate, timeout: float = 20.0, what: str = "condition"):
    return wait_until(predicate, timeout=timeout, what=what)


class ThreadLauncher:
    """``device.launch`` replaced by a thread running the same ``run_task`` the real run process runs; a run is
    alive while its thread is (``device.alive``)."""

    def __init__(self):
        self.threads: dict[tuple[str, int], threading.Thread] = {}

    def __call__(self, worker_root: Path, task, run: int) -> None:
        thread = threading.Thread(target=device.run_task, args=(worker_root, task.id, run), daemon=True)
        self.threads[(task.id, run)] = thread
        thread.start()

    def is_alive(self, task, run: int) -> bool:
        thread = self.threads.get((task.id, run))
        return bool(thread and thread.is_alive())

    def install(self, monkeypatch) -> ThreadLauncher:
        monkeypatch.setattr(device, "launch", self)
        monkeypatch.setattr(device, "alive", self.is_alive)
        return self

    def join(self, timeout: float = 20.0) -> None:
        for thread in self.threads.values():
            thread.join(timeout)
        assert not any(t.is_alive() for t in self.threads.values()), "a run thread did not end"


def brief(tmp: Path, text: str, name: str = "brief.md") -> str:
    path = tmp / name
    path.write_text(text, encoding="utf-8")
    return str(path)


def as_json(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def notified(job: dict) -> list[str]:
    """The ends the hub recorded on *job*, each as ``<end state or event>:<run>`` (``done:1``, ``question:1``)."""
    return [f"{entry.get('end_state') or entry['event']}:{entry['run']}" for entry in job.get("notified") or []]
