#!/usr/bin/env -S bash -c 'exec "$BASH" "$(dirname "$0")/run-in-hermes-env" python3 "$0" "$@"'
"""Acceptance script: does each of the owner's agents work as it does today?

One run checks every agent named in a manifest (see ``agent_acceptance.example.yaml``)
through the same surfaces a person uses, so the identical manifest can be pointed at
today's Hermes (``command: hermes``) and later at LINA (``command: lina``):

  gateway      a message in and a reply out through the gateway's api_server channel
  model        ``<command> -p <agent> chat -Q -q`` answers with a fresh nonce
  memory       the agent recalls a fact from its memory (configured prompt + expected text)
  person_card  the agent knows the person card (configured prompt + expected text)
  cron         ``cron list`` shows the expected jobs and ``cron status`` reports a live scheduler
  plugins      ``plugins list --json`` shows the expected plugins enabled
  work         the configured work hand-off command (``lina work run ...``) succeeds

A check with no configuration fails instead of being skipped: an unset expectation
cannot prove that the agent still works. Exit codes: 0 all checks passed, 1 a check
failed, 2 the manifest or the arguments are invalid.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
import uuid
from dataclasses import asdict, dataclass
from pathlib import Path
from typing import Any, Callable

sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from hermes_yaml import YAMLError, safe_load

CHECKS = ("gateway", "model", "memory", "person_card", "cron", "plugins", "work")
DEFAULT_TIMEOUT_S = 300
DEFAULT_GATEWAY_URL = "http://127.0.0.1:8642"
DEFAULT_GATEWAY_KEY_ENV = "API_SERVER_KEY"
NONCE_PROMPT = "Reply with exactly this token and nothing else: {nonce}"
DETAIL_LIMIT = 300


class ManifestError(ValueError):
    """The manifest cannot be used to run the checks."""


@dataclass
class CheckResult:
    agent: str
    check: str
    passed: bool
    detail: str


@dataclass
class Settings:
    command: str
    timeout: int
    gateway: dict[str, Any]


def _clip(text: str) -> str:
    text = " ".join(text.split())
    return text if len(text) <= DETAIL_LIMIT else text[: DETAIL_LIMIT - 3] + "..."


def _run(argv: list[str], timeout: int) -> tuple[int, str]:
    """Run one command; a missing binary or a timeout is a failed run, not a crash."""
    try:
        proc = subprocess.run(
            argv, capture_output=True, check=False, text=True, timeout=timeout, stdin=subprocess.DEVNULL,
            encoding="utf-8", errors="replace")
    except FileNotFoundError:
        return 127, f"command not found: {argv[0]}"
    except subprocess.TimeoutExpired:
        return 124, f"timed out after {timeout}s: {' '.join(argv)}"
    return proc.returncode, (proc.stdout or "") + (proc.stderr or "")


def _missing(text: str, expected: list[str]) -> list[str]:
    folded = text.casefold()
    return [item for item in expected if item.casefold() not in folded]


def _chat(settings: Settings, agent: str, prompt: str) -> tuple[int, str]:
    return _run([settings.command, "-p", agent, "chat", "-Q", "-q", prompt], settings.timeout)


def _expect_list(section: dict[str, Any], key: str) -> list[str]:
    value = section.get(key)
    if isinstance(value, str):
        return [value]
    if isinstance(value, list) and all(isinstance(item, str) for item in value):
        return value
    return []


def _prompt_check(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    prompt = section.get("prompt")
    expected = _expect_list(section, "expect")
    if not isinstance(prompt, str) or not prompt.strip() or not expected:
        return False, "not configured: set prompt and expect"
    code, output = _chat(settings, agent, prompt)
    if code != 0:
        return False, f"chat exited {code}: {_clip(output)}"
    missing = _missing(output, expected)
    if missing:
        return False, f"reply lacks {missing}: {_clip(output)}"
    return True, f"reply contains {expected}"


def check_model(settings: Settings, agent: str, _section: dict[str, Any]) -> tuple[bool, str]:
    nonce = f"ACCEPT-{uuid.uuid4().hex[:12]}"
    code, output = _chat(settings, agent, NONCE_PROMPT.format(nonce=nonce))
    if code != 0:
        return False, f"chat exited {code}: {_clip(output)}"
    if nonce not in output:
        return False, f"reply lacks the nonce {nonce}: {_clip(output)}"
    return True, "model answered with the nonce"


def check_memory(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    return _prompt_check(settings, agent, section)


def check_person_card(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    return _prompt_check(settings, agent, section)


def _gateway_base(settings: Settings, agent: str, section: dict[str, Any]) -> str:
    if section.get("url"):
        return str(section["url"]).rstrip("/")
    base = str(settings.gateway.get("url") or DEFAULT_GATEWAY_URL).rstrip("/")
    # A multiplexed gateway serves every non-default profile under /p/<profile>/.
    return base if agent == "default" else f"{base}/p/{agent}"


def _gateway_post(url: str, key: str, body: dict[str, Any], timeout: int) -> tuple[int, str]:
    request = urllib.request.Request(
        url, data=json.dumps(body).encode("utf-8"), method="POST",
        headers={"Authorization": f"Bearer {key}", "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return response.status, response.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read().decode("utf-8", "replace")
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        return 0, str(exc)


def _reply_text(payload: str) -> str:
    try:
        data = json.loads(payload)
        return str(data["choices"][0]["message"]["content"] or "")
    except (ValueError, KeyError, IndexError, TypeError):
        return ""


def check_gateway(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    key_env = str(section.get("key_env") or settings.gateway.get("key_env") or DEFAULT_GATEWAY_KEY_ENV)
    key = os.environ.get(key_env, "").strip()
    if not key:
        return False, f"not configured: the gateway key is not in ${key_env}"
    url = _gateway_base(settings, agent, section) + "/v1/chat/completions"
    nonce = f"ACCEPT-{uuid.uuid4().hex[:12]}"
    body = {"model": str(section.get("model") or "hermes-agent"),
            "messages": [{"role": "user", "content": NONCE_PROMPT.format(nonce=nonce)}]}
    status, payload = _gateway_post(url, key, body, settings.timeout)
    if status != 200:
        return False, f"POST {url} -> {status or 'no response'}: {_clip(payload)}"
    reply = _reply_text(payload)
    if nonce not in reply:
        return False, f"reply from {url} lacks the nonce {nonce}: {_clip(reply or payload)}"
    return True, f"round trip through {url}"


def _scheduler_healthy(output: str) -> bool:
    # `cron status` marks a live scheduler with a check mark and every problem with a cross or warning.
    return "✓" in output and "✗" not in output and "⚠" not in output


def check_cron(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    jobs = _expect_list(section, "jobs")
    if not jobs:
        return False, "not configured: list the expected cron job names under jobs"
    code, listing = _run([settings.command, "-p", agent, "cron", "list"], settings.timeout)
    if code != 0:
        return False, f"cron list exited {code}: {_clip(listing)}"
    missing = _missing(listing, jobs)
    if missing:
        return False, f"cron list lacks {missing}"
    code, status = _run([settings.command, "-p", agent, "cron", "status"], settings.timeout)
    if code != 0 or not _scheduler_healthy(status):
        return False, f"cron status is not healthy (exit {code}): {_clip(status)}"
    return True, f"{len(jobs)} job(s) listed and the scheduler is live"


def _enabled_plugins(payload: str) -> set[str] | None:
    try:
        data = json.loads(payload)
    except ValueError:
        return None
    rows = data.get("plugins", data) if isinstance(data, dict) else data
    if not isinstance(rows, list):
        return None
    names: set[str] = set()
    for row in rows:
        if isinstance(row, dict) and (row.get("enabled") is True or row.get("status") == "enabled"):
            names.update(str(row[k]) for k in ("name", "key") if row.get(k))
    return names


def check_plugins(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    expected = _expect_list(section, "enabled")
    if not expected:
        return False, "not configured: list the plugins that must be enabled under enabled"
    code, output = _run([settings.command, "-p", agent, "plugins", "list", "--json"], settings.timeout)
    if code != 0:
        return False, f"plugins list exited {code}: {_clip(output)}"
    enabled = _enabled_plugins(output)
    if enabled is None:
        return False, f"plugins list --json is not a plugin list: {_clip(output)}"
    missing = [name for name in expected if name not in enabled]
    if missing:
        return False, f"not enabled: {missing}"
    return True, f"{len(expected)} plugin(s) enabled"


def check_work(settings: Settings, agent: str, section: dict[str, Any]) -> tuple[bool, str]:
    argv = section.get("argv")
    if not isinstance(argv, list) or not argv or not all(isinstance(a, str) for a in argv):
        return False, "not configured: set argv to the work hand-off command"
    argv = [arg.replace("{agent}", agent) for arg in argv]
    code, output = _run(argv, settings.timeout)
    if code != 0:
        return False, f"{' '.join(argv)} exited {code}: {_clip(output)}"
    missing = _missing(output, _expect_list(section, "expect"))
    if missing:
        return False, f"output lacks {missing}: {_clip(output)}"
    return True, f"{' '.join(argv)} succeeded"


CHECK_FUNCS: dict[str, Callable[[Settings, str, dict[str, Any]], tuple[bool, str]]] = {
    "gateway": check_gateway, "model": check_model, "memory": check_memory,
    "person_card": check_person_card, "cron": check_cron, "plugins": check_plugins,
    "work": check_work,
}


def load_manifest(path: Path) -> tuple[Settings, list[dict[str, Any]]]:
    try:
        data = safe_load(path.read_text(encoding="utf-8-sig"))
    except (OSError, YAMLError) as exc:
        raise ManifestError(f"cannot read {path}: {exc}") from exc
    if not isinstance(data, dict):
        raise ManifestError("the manifest must be a mapping")
    agents = data.get("agents")
    if not isinstance(agents, list) or not agents:
        raise ManifestError("agents must be a non-empty list")
    for agent in agents:
        if not isinstance(agent, dict) or not isinstance(agent.get("name"), str) or not agent["name"]:
            raise ManifestError(f"every agent needs a name: {agent!r}")
    gateway = data.get("gateway") or {}
    if not isinstance(gateway, dict):
        raise ManifestError("gateway must be a mapping")
    timeout = data.get("timeout", DEFAULT_TIMEOUT_S)
    if not isinstance(timeout, int) or timeout <= 0:
        raise ManifestError("timeout must be a positive number of seconds")
    return Settings(str(data.get("command") or "hermes"), timeout, gateway), agents


def run_checks(settings: Settings, agents: list[dict[str, Any]], checks: list[str]) -> list[CheckResult]:
    results = []
    for agent in agents:
        for check in checks:
            section = agent.get(check) or {}
            if not isinstance(section, dict):
                results.append(CheckResult(agent["name"], check, False, f"{check} must be a mapping"))
                continue
            passed, detail = CHECK_FUNCS[check](settings, agent["name"], section)
            results.append(CheckResult(agent["name"], check, passed, detail))
            print(f"{'PASS' if passed else 'FAIL'}  {agent['name']:<16} {check:<12} {detail}", flush=True)
    return results


def _parse_args(argv: list[str] | None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Check that each agent works as it does today.")
    parser.add_argument("--manifest", required=True, type=Path, help="Path to the agents manifest (YAML)")
    parser.add_argument("--agent", action="append", default=[], help="Only this agent (repeatable)")
    parser.add_argument("--check", action="append", default=[], choices=CHECKS,
                        help="Only this check (repeatable); default: all checks")
    parser.add_argument("--json", dest="json_path", type=Path, help="Also write the results as JSON here")
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = _parse_args(argv)
    try:
        settings, agents = load_manifest(args.manifest)
    except ManifestError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    if args.agent:
        unknown = sorted(set(args.agent) - {a["name"] for a in agents})
        if unknown:
            print(f"error: not in the manifest: {unknown}", file=sys.stderr)
            return 2
        agents = [a for a in agents if a["name"] in args.agent]
    checks = [c for c in CHECKS if not args.check or c in args.check]
    results = run_checks(settings, agents, checks)
    failed = [r for r in results if not r.passed]
    print(f"\n{len(results) - len(failed)}/{len(results)} checks passed for {len(agents)} agent(s)")
    if args.json_path:
        args.json_path.write_text(json.dumps([asdict(r) for r in results], indent=2) + "\n", encoding="utf-8")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
