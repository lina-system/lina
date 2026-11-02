#!/usr/bin/env -S bash -c 'exec "$BASH" "$(dirname "$0")/run-in-hermes-env" python3 "$0" "$@"'
"""Acceptance check for the shared Desk (lina-system/lina#44), on a fresh install.

In a new temporary home it runs, through the real entry points:

  install    ``<command> desk init`` creates the Desk at ``~/Desk``
  layout     the PARA folders and AGENTS.md exist and ``desk.path`` names the Desk
  agents     ``<command> profile create`` adds a second agent
  workdir    both agents' working directory (as the gateway resolves it) is the Desk
  shared     agent A writes a Desk file with the file tools, agent B reads it and writes it,
             and agent A reads B's change, each in its own process
  api        ``GET /api/desk`` returns the Desk and ``/api/desk/resolve`` names the shared file
             and refuses a path off the Desk

Exit codes: 0 every check passed, 1 a check failed. ``--json`` writes the results.
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
import tempfile
import uuid
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent
SHARED_FILE = "Inbox/desk-acceptance.md"
SECOND_AGENT = "desk-b"
TIMEOUT_S = 300


def _agent_turn(action: str, text: str) -> int:
    """One agent's step, run in its own process with that agent's HERMES_HOME."""
    sys.path.insert(0, str(REPO))
    from tools.terminal_scope import build_profile_terminal_scope

    cwd = build_profile_terminal_scope(os.environ["HERMES_HOME"]).get("TERMINAL_CWD", "")
    if action == "cwd":
        print(cwd)
        return 0
    if action == "api":
        return _api_turn()
    os.environ["TERMINAL_CWD"] = cwd
    from model_tools import handle_function_call

    task = f"desk-acceptance-{uuid.uuid4().hex[:8]}"
    read = json.loads(handle_function_call("read_file", {"path": SHARED_FILE}, task_id=task))
    if action == "read":
        print(read.get("content", ""))
        return 0
    kept = [] if "error" in read else [line.split("|", 1)[-1] for line in read.get("content", "").splitlines()]
    content = "".join(f"{line}\n" for line in [*kept, text])
    result = json.loads(handle_function_call("write_file", {"path": SHARED_FILE, "content": content}, task_id=task))
    print(json.dumps(result))
    return 0 if "error" not in result else 1


def _api_turn() -> int:
    from starlette.testclient import TestClient

    from hermes_cli import web_server

    web_server.app.state.auth_required = False
    client = TestClient(web_server.app)
    client.headers[web_server._SESSION_HEADER_NAME] = web_server._SESSION_TOKEN
    desk = client.get("/api/desk")
    shared = client.get("/api/desk/resolve", params={"ref": "desk:" + SHARED_FILE})
    escape = client.get("/api/desk/resolve", params={"ref": "desk:../outside.txt"})
    print(json.dumps({"desk": [desk.status_code, desk.json()], "shared": [shared.status_code, shared.json()],
                      "escape": escape.status_code}))
    return 0


def _run(argv: list[str], env: dict[str, str]) -> tuple[int, str]:
    proc = subprocess.run(argv, capture_output=True, text=True, encoding="utf-8", errors="replace", env=env,
                          timeout=TIMEOUT_S, check=False, stdin=subprocess.DEVNULL, cwd=REPO)
    return proc.returncode, (proc.stdout + proc.stderr).strip()


def _turn(home: Path, base_env: dict[str, str], action: str, text: str = "") -> tuple[int, str]:
    env = dict(base_env, HERMES_HOME=str(home))
    return _run([sys.executable, str(Path(__file__).resolve()), "--agent-turn", action, "--text", text], env)


def run_checks(command: list[str], root: Path) -> list[dict]:
    home = root / "home"
    hermes_home = home / ".hermes"
    hermes_home.mkdir(parents=True)
    env = {k: v for k, v in os.environ.items() if not k.startswith(("HERMES_", "TERMINAL_"))}
    env.update(HOME=str(home), HERMES_HOME=str(hermes_home), NO_COLOR="1")
    desk = home / "Desk"
    second = hermes_home / "profiles" / SECOND_AGENT
    results: list[dict] = []

    def check(name: str, passed: bool, detail: str) -> bool:
        results.append({"check": name, "passed": bool(passed), "detail": detail[-600:]})
        return passed

    rc, out = _run([*command, "desk", "init"], env)
    if not check("install", rc == 0, out):
        return results
    config_file = hermes_home / "config.yaml"
    config = config_file.read_text(encoding="utf-8-sig") if config_file.exists() else ""
    missing = [n for n in ("Inbox", "Projects", "Areas", "Resources", "Archive", "AGENTS.md") if not (desk / n).exists()]
    check("layout", not missing and f"path: {desk}" in config, f"missing={missing}")
    rc, out = _run([*command, "profile", "create", SECOND_AGENT, "--no-alias"], env)
    if not check("agents", rc == 0 and second.is_dir(), out):
        return results
    cwds = {str(h): _turn(h, env, "cwd") for h in (hermes_home, second)}
    check("workdir", all(rc == 0 and out.splitlines()[-1:] == [str(desk)] for rc, out in cwds.values()), json.dumps(cwds))
    nonce_a, nonce_b = f"A-{uuid.uuid4().hex[:8]}", f"B-{uuid.uuid4().hex[:8]}"
    steps = [_turn(hermes_home, env, "write", nonce_a), _turn(second, env, "read"),
             _turn(second, env, "write", nonce_b), _turn(hermes_home, env, "read")]
    text = (desk / SHARED_FILE).read_text(encoding="utf-8-sig") if (desk / SHARED_FILE).exists() else ""
    check("shared", all(rc == 0 for rc, _ in steps) and nonce_a in steps[1][1] and nonce_b in steps[3][1]
          and text == f"{nonce_a}\n{nonce_b}\n", json.dumps({"steps": steps, "file": text}))
    rc, out = _turn(hermes_home, env, "api")
    try:
        api = json.loads(out.splitlines()[-1])
        ok = (api["desk"][0] == 200 and api["desk"][1]["path"] == str(desk) and api["shared"][0] == 200
              and api["shared"][1]["path"] == str(desk / SHARED_FILE) and api["shared"][1]["exists"]
              and api["escape"] == 400)
    except (ValueError, KeyError, IndexError, TypeError):
        ok = False
    check("api", rc == 0 and ok, out)
    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--command", default=None,
                        help="Command under test (default: this checkout's hermes, run with this Python)")
    parser.add_argument("--json", type=Path, help="Write the results as JSON here")
    parser.add_argument("--keep", action="store_true", help="Keep the temporary install for inspection")
    parser.add_argument("--agent-turn", choices=("cwd", "read", "write", "api"), help=argparse.SUPPRESS)
    parser.add_argument("--text", default="", help=argparse.SUPPRESS)
    args = parser.parse_args(argv)
    if args.agent_turn:
        return _agent_turn(args.agent_turn, args.text)
    command = shlex.split(args.command) if args.command else [sys.executable, str(REPO / "hermes")]
    root = Path(tempfile.mkdtemp(prefix="desk-acceptance-"))
    try:
        results = run_checks(command, root)
    finally:
        if not args.keep:
            import shutil
            shutil.rmtree(root, ignore_errors=True)
    for row in results:
        print(f"{'PASS' if row['passed'] else 'FAIL'}  {row['check']}" + ("" if row["passed"] else f"  {row['detail']}"))
    if args.keep:
        print(f"install kept at {root}")
    if args.json:
        args.json.write_text(json.dumps(results, indent=2), encoding="utf-8")
    passed = len(results) == 6 and all(row["passed"] for row in results)
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
