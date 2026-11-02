"""``lina models refresh`` and its cron entry (lina#33): the credit refresh that keeps pooled accounts' quota
observations fresh, run as a LINA cron job every 2 minutes instead of the old OCX's own refresh job.

A refresh is ``ocx account refresh <provider> --json`` of the pinned CLI for each pooled provider: those whose rows
in ``ocx account list --json`` are a Codex pool (``codex``) or an OAuth pool (``oauth``). It runs only against
LINA's own OCX (``own_proxy_running``), never the 10100 fallback of OCX's CLI.

The cron entry is created disabled (by ``takeover copy``), so two instances never refresh the same OAuth accounts
during a rehearsal; ``takeover switch`` enables it after a good switch.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

from hermes_cli import lina_ocx

JOB_NAME = "lina-ocx-credit-refresh"
SCRIPT_NAME = "lina-ocx-refresh.py"
SCHEDULE = "every 2m"
POOLED_TYPES = ("codex", "oauth")
TIMEOUT_S = 120
_SCRIPT = """import sys

from hermes_cli.lina_ocx_takeover_refresh import refresh

sys.exit(refresh())
"""


def pooled_providers(accounts: dict) -> list[str]:
    names: list[str] = []
    for row in accounts.get("accounts", []):
        provider = row.get("provider")
        if row.get("type") in POOLED_TYPES and provider and provider not in names:
            names.append(provider)
    return names


def refresh(providers: list[str] | None = None, root: Path | None = None, out=print) -> int:
    from hermes_cli.lina_models import query
    root = root or lina_ocx.lina_root()
    version = lina_ocx.current_version(root)
    if version is None:
        out("refresh: OCX is not installed. Run `lina models install`.")
        return 1
    try:
        providers = providers or pooled_providers(query("accounts", root))
    except lina_ocx.OcxError as exc:
        out(f"refresh: {exc}")
        return 1
    failed = []
    for provider in providers:
        try:
            result = subprocess.run(lina_ocx.ocx_argv(root, version, "account", "refresh", provider, "--json"),
                                    env=lina_ocx.ocx_env(root), capture_output=True, text=True, encoding="utf-8",
                                    errors="replace", timeout=TIMEOUT_S, check=False)
            ok, detail = result.returncode == 0, (result.stderr or result.stdout).strip().splitlines()[-1:]
        except subprocess.TimeoutExpired:
            ok, detail = False, [f"took over {TIMEOUT_S}s"]
        if not ok:
            failed.append(provider)
        out(f"{'✓' if ok else '✗'} {provider}" + ("" if ok else f": {detail[0] if detail else 'failed'}"))
    if not providers:
        out("refresh: no pooled providers")
    return 1 if failed else 0


def _find(jobs: list[dict]) -> dict | None:
    return next((job for job in jobs if job.get("name") == JOB_NAME), None)


def ensure_job(root: Path, out=print) -> dict:
    """The cron entry in the LINA root's cron store, created disabled when missing; an existing one is kept as is."""
    from cron import jobs
    with jobs.use_cron_store(root):
        existing = _find(jobs.list_jobs(include_disabled=True))
        if existing is not None:
            return existing
        scripts = root / "scripts"
        scripts.mkdir(mode=0o700, parents=True, exist_ok=True)
        (scripts / SCRIPT_NAME).write_text(_SCRIPT, encoding="utf-8")
        job = jobs.create_job(None, SCHEDULE, name=JOB_NAME, script=SCRIPT_NAME, no_agent=True, deliver="local",
                              paused=True, paused_reason="enabled by `lina models takeover switch`")
    out(f"✓ cron entry {JOB_NAME} ({SCHEDULE}) created disabled; `lina models takeover switch` enables it")
    return job


def enable_job(root: Path, out=print) -> None:
    from cron import jobs
    ensure_job(root, out=lambda line: None)
    with jobs.use_cron_store(root):
        jobs.resume_job(JOB_NAME)
    out(f"✓ cron entry {JOB_NAME} enabled ({SCHEDULE})")
