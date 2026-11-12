"""The board's quota view (lina#39) from LINA's own OCX: per service its accounts, plan, paused, each quota window's
percent and reset, and the burn rate in percent per hour.

The accounts are ``lina_models.query("accounts")`` (the quota OCX caches per account; the probing ``quota`` view stays
CLI-only). An OCX run outside LINA (``ocx.external``, lina#232) is read with ``account list --quota --json``: OCX 2.97
puts no quota in the plain list. OCX keeps each account's quota for 10 minutes (``ACCOUNT_QUOTA_TTL_MS``,
``src/providers/quota-wire.ts`` in OCX 2.97); after that a read probes the provider for that account, which may refresh
its OAuth token, and ``QUOTA_EVERY = 600`` keeps LINA at one such read per 10 minutes. The rate needs history, so
each read appends one row per account window to ``board/quota.jsonl`` under the LINA root, at most every ten minutes, and rows older than 14 days are dropped. Rate rules (the board's
``model/fleet.py``): over the last three hours inside one reset period, at least 30 minutes of records.

When ``worker.proxy`` reads magpie (lina#588, ``lina_models_proxy``), its quota cards (``/v1/magpie/quotas``, what
magpie last read) come in as accounts of the same shape (``lina_magpie.account``) and go through the same view and
history. With more than one proxy read each service carries ``proxy: <kind>``; a proxy that cannot be read adds an
error and shows nothing (unknown), while the others still show.
"""

from __future__ import annotations

import hashlib
import json
import threading
import time
from datetime import datetime
from pathlib import Path

QUOTA_EVERY = 600
QUOTA_KEEP = 14 * 86400
QUOTA_SPAN = 3 * 3600
SERVICES = (("OpenAI", ("openai", "codex", "chatgpt")), ("Claude", ("claude", "anthropic")), ("Grok", ("grok", "xai")),
            ("Gemini", ("gemini",)), ("Antigravity", ("antigravity",)), ("Cursor", ("cursor",)))
WINDOWS = (("weekly", "weeklyPercent", "weeklyResetAt"), ("fiveHour", "fiveHourPercent", "fiveHourResetAt"),
           ("monthly", "monthlyPercent", "monthlyResetAt"))
# The window a service is read by: weekly, else monthly; Antigravity by its Gemini weekly window (OCX gives it no
# weeklyPercent).
MAIN_WINDOW = {"Antigravity": "custom:Gem (Weekly)"}
# Providers named exactly, so ``google-antigravity`` stays Antigravity: ``google`` is a Gemini API key.
EXACT_SERVICE = {"google": "Gemini"}


def _number(value) -> float | None:
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def windows(acct: dict) -> list[tuple[str, float, object]]:
    """(key, percent, resetAt) of each quota window of an OCX account: weekly, five-hour, monthly, then per model."""
    quota = acct.get("quota") if isinstance(acct.get("quota"), dict) else {}
    out = [(key, _number(quota.get(value)), quota.get(reset)) for key, value, reset in WINDOWS
           if _number(quota.get(value)) is not None]
    out += [("custom:" + str(row.get("label")), _number(row.get("percent")), row.get("resetAt"))
            for row in quota.get("customWindows") or [] if isinstance(row, dict) and _number(row.get("percent")) is not None]
    return out


def reset_time(value) -> float | None:
    """OCX reset time: seconds or milliseconds since the epoch, or an ISO time."""
    number = _number(value)
    if number is not None:
        return number / 1000 if number > 1e11 else number if number > 0 else None
    try:
        return datetime.fromisoformat(str(value)).timestamp()
    except ValueError:
        return None


def burn(rows: list[dict], now: float) -> float | None:
    """%/hour over the last three hours of one account window, within its current reset period: a new reset time or a
    falling percent starts the count again. None when the usable records span less than 30 minutes."""
    period: list[dict] = []
    for row in sorted((r for r in rows if _number(r.get("percent")) is not None), key=lambda r: r["at"]):
        if period:
            before, after = reset_time(period[-1].get("resetAt")), reset_time(row.get("resetAt"))
            moved = (before is None) != (after is None) or before is not None and abs(after - before) > 600
            if moved or row["percent"] < period[-1]["percent"]:
                period = []
        period.append(row)
    period = [row for row in period if now - row["at"] <= QUOTA_SPAN]
    if len(period) < 2 or period[-1]["at"] - period[0]["at"] < 1800:
        return None
    return (period[-1]["percent"] - period[0]["percent"]) / ((period[-1]["at"] - period[0]["at"]) / 3600)


def runs_out(percent: float | None, rate: float | None, reset, now: float) -> bool:
    """At this rate the window reaches 100% before it resets."""
    at = reset_time(reset)
    return (percent is not None and rate is not None and rate > 0 and at is not None and at > now
            and percent + rate * (at - now) / 3600 >= 100)


def service_of(provider) -> str:
    text = str(provider or "").lower()
    if text in EXACT_SERVICE:
        return EXACT_SERVICE[text]
    return next((name for name, keys in SERVICES if any(key in text for key in keys)), str(provider or ""))


def account_key(acct: dict) -> str:
    """A hash of the account id, so an address used as an id never leaves the host."""
    return hashlib.sha256(str(acct.get("id")).encode()).hexdigest()[:12]


def _mean(values: list[float]) -> float | None:
    values = [v for v in values if v is not None]
    return sum(values) / len(values) if values else None


def _account(acct: dict, history: dict, n: int, name: str, now: float) -> dict:
    key = account_key(acct)
    rows = []
    for window, percent, reset in windows(acct):
        rate = burn(history.get((acct.get("provider"), key, window), []), now)
        rows.append({"key": window, "percent": percent, "left": min(100.0, max(0.0, 100.0 - percent)),
                     "resetAt": reset_time(reset), "ratePerHour": rate,
                     "warn": not acct.get("paused") and runs_out(percent, rate, reset, now)})
    pick = MAIN_WINDOW.get(name)
    main = next((row for row in rows if row["key"] == pick), None) if pick else (
        next((row for row in rows if row["key"] == "weekly"), None)
        or next((row for row in rows if row["key"] == "monthly"), None))
    five = next((row for row in rows if row["key"] == "fiveHour"), None)
    state = "reauth" if acct.get("needsReauth") else "paused" if acct.get("paused") else "ok"
    return {"id": key, "number": n, "plan": acct.get("plan") or "", "paused": bool(acct.get("paused")),
            "active": bool(acct.get("active")), "state": state,
            "unavailable": bool((acct.get("quota") or {}).get("quotaUnavailable")), "windows": rows,
            "mainWindow": main["key"] if main else None,
            "warn": any(row["warn"] for row in (main, five) if row), "_main": main}


def quota_view(accounts: list[dict], history: list[dict], now: float) -> list[dict]:
    """Services in a fixed order with their accounts (numbered per service), and the service mean of the main
    window's percent and rate over the accounts that are not paused."""
    by_window: dict[tuple, list[dict]] = {}
    for row in history:
        by_window.setdefault((row.get("provider"), row.get("id"), row.get("window")), []).append(row)
    groups: dict[str, list[dict]] = {}
    for acct in accounts:
        if not windows(acct) and acct.get("type") != "api-key" and not acct.get("needsReauth") \
                and not acct.get("proxy"):
            continue  # OCX reads nothing for it; API keys, accounts to sign in again and magpie's cards stay listed
        name = service_of(acct.get("provider"))
        group = groups.setdefault(name, [])
        group.append(_account(acct, by_window, len(group) + 1, name, now))
    order = [name for name, _ in SERVICES]
    out = []
    for name in sorted(groups, key=lambda n: (order.index(n) if n in order else len(order), n)):
        live = [acct["_main"] for acct in groups[name] if not acct["paused"] and acct["_main"]]
        out.append({"service": name, "percent": _mean([m["percent"] for m in live]),
                    "ratePerHour": _mean([m["ratePerHour"] for m in live]),
                    "accounts": [{k: v for k, v in acct.items() if k != "_main"} for acct in groups[name]]})
    return out


class QuotaLog:
    """``board/quota.jsonl``: one row per account window per read, rows older than 14 days dropped."""

    def __init__(self, path: Path):
        self.path = path

    def rows(self, now: float) -> list[dict]:
        if not self.path.is_file():
            return []
        out = []
        for line in self.path.read_text(encoding="utf-8-sig").splitlines():
            try:
                row = json.loads(line)
            except ValueError:
                continue  # a line cut short by a crash mid-append
            if isinstance(row, dict) and isinstance(row.get("at"), (int, float)) and now - row["at"] <= QUOTA_KEEP:
                out.append(row)
        return out

    def add(self, accounts: list[dict], now: float) -> list[dict]:
        """Append this read and drop old rows; -> every kept row."""
        kept = self.rows(now)
        new = [{"at": now, "provider": acct.get("provider"), "id": account_key(acct), "window": window,
                "percent": percent, "resetAt": reset} for acct in accounts for window, percent, reset in windows(acct)]
        self.path.parent.mkdir(parents=True, exist_ok=True)
        tmp = self.path.with_suffix(".tmp")
        tmp.write_text("".join(json.dumps(row, ensure_ascii=False) + "\n" for row in kept + new), encoding="utf-8")
        tmp.replace(self.path)
        return kept + new


_CACHE: dict[Path, tuple[float, list[dict]]] = {}
_LOCK = threading.Lock()


def missing(accounts: list[dict]) -> list[str]:
    """The services of ``SERVICES`` OCX holds no account for."""
    held = {service_of(acct.get("provider")) for acct in accounts}
    return [name for name, _ in SERVICES if name not in held]


def read(root: Path, now: float | None = None) -> list[dict]:
    """The quota view, reading OCX at most every ten minutes per LINA root (raises ``lina_ocx.OcxError``, also when
    one of several proxies read fails: the board snapshot then shows its quota part as unread)."""
    return view(root, now)["quota"]


def _ocx_accounts(root: Path) -> list[dict]:
    from hermes_cli.lina_models import query
    from hermes_cli.lina_ocx import external
    data = query("quota" if external(root) else "accounts", root)
    return [a for a in (data.get("accounts") if isinstance(data, dict) else None) or [] if isinstance(a, dict)]


def _accounts(kind: str, root: Path, proxy, now: float, log: QuotaLog) -> list[dict]:
    """One proxy's accounts, read at most every ten minutes per LINA root; a failed read is not kept."""
    from hermes_cli import lina_magpie
    key = root if kind == "ocx" else (root, kind)
    cached = _CACHE.get(key)
    if cached is None or now - cached[0] >= QUOTA_EVERY:
        accounts = _ocx_accounts(root) if kind == "ocx" else lina_magpie.quota_accounts(proxy)
        log.add(accounts, now)
        _CACHE[key] = cached = (now, accounts)
    return cached[1]


def view(root: Path, now: float | None = None, errors: list[dict] | None = None) -> dict:
    """``{quota, missing}``: the quota view and the services without an account. With more than one proxy read, one
    that fails adds ``{source, message}`` to *errors* (raised when there is no list, or when none answered)."""
    from hermes_cli.lina_models_proxy import node_proxy
    from hermes_cli.lina_ocx import OcxError
    now = time.time() if now is None else now
    proxy = node_proxy(root)
    kinds = proxy.reads()
    read: dict[str, list[dict]] = {}
    with _LOCK:
        log = QuotaLog(root / "board" / "quota.jsonl")
        for kind in kinds:
            try:
                read[kind] = _accounts(kind, root, proxy, now, log)
            except OcxError as exc:
                if errors is None or len(kinds) == 1 or (not read and kind == kinds[-1]):
                    raise
                errors.append({"source": f"quota-{kind}", "message": f"unknown: {exc}"[:300]})
        history = [row for row in log.rows(now) if now - row["at"] <= QUOTA_SPAN + QUOTA_EVERY]
    quota = []
    for kind, accounts in read.items():
        services = quota_view(accounts, history, now)
        quota += [{**service, "proxy": kind} for service in services] if len(kinds) > 1 else services
    held = missing([acct for accounts in read.values() for acct in accounts])
    return {"quota": quota, "missing": held if len(read) == len(kinds) else None}  # unknown while one is unread
