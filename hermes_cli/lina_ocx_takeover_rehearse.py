"""``lina models takeover rehearse`` (lina#33): the pinned OCX on LINA's copy, on a spare loopback port.

OCX 2.95.0 has no switch that turns "credit refresh" off: that refresh is the old proxy's external job (``ocx
account refresh`` every 2 minutes), and LINA's replacement cron entry stays disabled until ``switch``. What OCX does
on its own is turned off in the copy's ``config.json`` for the run (``tokenGuardian``, ``codexQuotaAutoRefresh``,
``catalogAutoRefresh``, ``quotaResetNotify``), together with every listener that would bind an old address or port
(``hostname`` becomes 127.0.0.1, ``unauthenticatedLoopbackListener`` and hub ``managementIngress`` off, ``port`` the
spare port). The copy's ``config.json`` is restored byte for byte afterwards.

A copied data dir still points at the same OS keychain items, so on a host with real ``keychain:`` references only
the operator runs a rehearsal.
"""

from __future__ import annotations

import json
import subprocess
import time
from pathlib import Path

from hermes_cli import lina_ocx, lina_ocx_takeover as takeover

REHEARSE_WAIT_S = 90.0
_OFF = {"tokenGuardian": {"enabled": False}, "catalogAutoRefresh": {"enabled": False},
        "quotaResetNotify": {"enabled": False}}


def rehearsal_config(config: dict, port: int) -> dict:
    """The copy's config for a rehearsal: loopback only on *port*, OCX's own background refreshers off."""
    edited = dict(config)
    edited.update(port=port, hostname="127.0.0.1", unauthenticatedLoopbackListener={"enabled": False},
                  codexQuotaAutoRefresh={})
    for key, off in _OFF.items():
        edited[key] = {**(config.get(key) if isinstance(config.get(key), dict) else {}), **off}
    if isinstance(config.get("hub"), dict):
        edited["hub"] = {**config["hub"], "managementIngress": {"enabled": False}}
    return edited


def _pool_rows(listing: dict) -> int:
    """Account rows that come from the data dir; the native Codex login row depends on HOME, not on the data."""
    return sum(1 for row in listing.get("accounts", []) if row.get("slotKind") != "native-main")


def _account_list(root: Path, version: str) -> dict:
    result = subprocess.run(lina_ocx.ocx_argv(root, version, "account", "list", "--json"), env=lina_ocx.ocx_env(root),
                            capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=60,
                            check=False)
    if result.returncode != 0:
        raise takeover.TakeoverError(f"ocx account list: {(result.stderr or result.stdout).strip()[-200:]}")
    return json.loads(result.stdout)


def _spare_port(taken: set[int]) -> int:
    port = lina_ocx.free_port()
    while port in taken:
        port = lina_ocx.free_port()
    return port


def _wait_healthy(root: Path, port: int, proc: subprocess.Popen) -> bool:
    deadline = time.monotonic() + REHEARSE_WAIT_S
    while time.monotonic() < deadline and proc.poll() is None:
        if lina_ocx.own_proxy_running(root, port) and lina_ocx.http_get(port, "/v1/models")[0] == 200:
            return True
        time.sleep(0.5)
    return False


def _stop(proc: subprocess.Popen) -> None:
    if proc.poll() is None:
        proc.terminate()
        try:
            proc.wait(timeout=15)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def _check(root: Path, version: str, port: int, proc: subprocess.Popen, expected: int, out) -> bool:
    if not _wait_healthy(root, port, proc):
        out(f"rehearse: GET /v1/models on 127.0.0.1:{port} never answered 200"
            + (f" (OCX exited {proc.returncode})" if proc.poll() is not None else ""))
        return False
    out(f"✓ GET /v1/models 200 on 127.0.0.1:{port}")
    got = _pool_rows(_account_list(root, version))
    if got != expected:
        out(f"rehearse: the copy lists {got} pooled accounts, the source {expected}")
        return False
    out(f"✓ ocx account list --json: {got} pooled accounts, as on the source")
    return True


def _expected_accounts(source_accounts: Path | None, expect: int | None) -> int:
    if expect is not None:
        return expect
    if source_accounts is None:
        raise takeover.TakeoverError("pass --source-accounts <file> (`ocx account list --json` of the old proxy) "
                                     "or --expect-accounts N")
    return _pool_rows(json.loads(source_accounts.read_text(encoding="utf-8-sig")))


def rehearse(source: Path, source_accounts: Path | None = None, expect: int | None = None,
             root: Path | None = None, out=print) -> int:
    root = root or lina_ocx.lina_root()
    version = lina_ocx.current_version(root)
    data = lina_ocx.data_dir(root)
    if version is None or not takeover.manifest_path(root).exists():
        out("rehearse: needs the pinned OCX (`lina models install`) and a copy (`lina models takeover copy`)")
        return 1
    try:
        expected = _expected_accounts(source_accounts, expect)
        old_ports = {item.port for item in takeover.listeners(takeover.read_config(source.expanduser()))}
        config_file = data / "config.json"
        original = config_file.read_bytes()
        port = _spare_port(old_ports)
        config_file.write_text(json.dumps(rehearsal_config(json.loads(original), port), indent=2) + "\n",
                               encoding="utf-8")
    except (takeover.TakeoverError, OSError, ValueError) as exc:
        out(f"rehearse: {exc}")
        return 1
    out(f"rehearsal of OCX {version} on {data}, loopback port {port} (old ports {sorted(old_ports)} untouched); "
        "credit refresh off")
    proc = None
    try:
        lina_ocx.write_start_port(data, port)
        proc = subprocess.Popen(lina_ocx.ocx_argv(root, version, "start", "--port", str(port)),
                                env=lina_ocx.ocx_env(root, data=data), stdout=subprocess.DEVNULL,
                                stderr=subprocess.DEVNULL)
        ok = _check(root, version, port, proc, expected, out)
    except (takeover.TakeoverError, lina_ocx.OcxError, ValueError) as exc:
        out(f"rehearse: {exc}")
        ok = False
    finally:
        if proc is not None:
            _stop(proc)
        config_file.write_bytes(original)
    out("✓ rehearsal passed; the rehearsal OCX is stopped" if ok else "rehearsal failed; the rehearsal OCX is stopped")
    return 0 if ok else 1
