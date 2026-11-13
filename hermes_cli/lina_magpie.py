"""A read-only magpie client (lina#588): usage, quota and the model list of the magpie gateway named by
``worker.proxy`` (``hermes_cli/worker/proxy.py``), in the shapes the screens render (``lina_models``). LINA never
installs, configures or restarts magpie: only three GETs are sent.

- ``GET /v1/models``: the OpenAI-style list (``{"object": "list", "data": [{"id": ...}]}``). Each id is shown in
  magpie's catalog form: a node's ``<remote_id>/<provider>/<id>`` reads ``<provider>/<id>``.
- ``GET /v1/magpie/usage?period=today|7d|30d``: the Usage page's summary (magpie ``internal/usageapi``): the period's
  totals (``calls``, ``input``, ``output``, ``cache_read``, ``cache_write``, ``cost``, ``unpriced``) and the same
  totals per model (``provider``, ``model``), per account (``provider``, ``account``, ``name``) and per caller key
  (``callerKeyId``, ``callerKeyName``, ``name``). A node is told apart by its caller key name, which becomes the
  ``machine`` of a ``byMachine`` row.
- ``GET /v1/magpie/quotas?kept=1``: ``{"object": "list", "data": [card]}``, each card as ``magpie quota --json``
  prints it (``provider``, ``name``, ``kind``, ``plan``, ``windows[]`` with ``name``, ``used``, ``remaining``,
  ``resetsAt``; ``readDeferred``, ``asOf``, ``error``). ``kept=1`` serves what magpie last read and asks no vendor.

Parsing is tolerant: unknown fields are ignored, a number magpie does not give is ``None`` (unknown), never 0. Every
read has a short timeout; an unreachable or erroring magpie raises :class:`MagpieError`, which the screens show as
unknown. Without ``worker.proxy.url`` the gateway is the origin of ``worker.proxy.health``, else magpie's default
port on 127.0.0.1. ``worker.proxy.key_env`` names the variable (a secret in ``.env``) holding a gateway key; magpie
answers its own machine without one.
"""

from __future__ import annotations

import hashlib
import json
import os
import urllib.error
import urllib.request
from urllib.parse import urlsplit

from hermes_cli.worker import proxy as proxies

DEFAULT_URL = "http://127.0.0.1:3425"  # magpie's default gateway address (its docs)
TIMEOUT_S = 5
PERIODS = ("today", "7d", "30d")
# magpie's window names -> the quota fields of an account (``lina_board_quota.WINDOWS``); any other window is a custom one.
WINDOW_FIELDS = {"5 hours": ("fiveHourPercent", "fiveHourResetAt"), "7 days": ("weeklyPercent", "weeklyResetAt"),
                 "30 days": ("monthlyPercent", "monthlyResetAt"), "month": ("monthlyPercent", "monthlyResetAt")}


class MagpieError(Exception):
    """magpie could not be read: what it would have said is unknown."""


def base_url(proxy: proxies.Proxy) -> str:
    if proxy.url:
        return proxy.url.rstrip("/")
    if proxy.health:
        parts = urlsplit(proxy.health)
        if parts.scheme and parts.netloc:
            return f"{parts.scheme}://{parts.netloc}"
    return DEFAULT_URL


def gateway_key(proxy: proxies.Proxy) -> str | None:
    """The gateway key ``worker.proxy.key_env`` names, None when unset: a secret of `.env`, never logged. The node's
    proxy check (``worker.device.proxy_check``, lina#625) sends the same key."""
    return os.environ.get(proxy.key_env) if proxy.key_env else None


def get(proxy: proxies.Proxy, path: str):
    """One GET of magpie's gateway, parsed; :class:`MagpieError` when it does not answer JSON with 200."""
    headers = {"Accept": "application/json"}
    key = gateway_key(proxy)
    if key:
        headers["Authorization"] = f"Bearer {key}"
    request = urllib.request.Request(base_url(proxy) + path, headers=headers, method="GET")
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))  # magpie is reached directly
    try:
        with opener.open(request, timeout=TIMEOUT_S) as response:
            body = response.read()
    except urllib.error.HTTPError as exc:
        raise MagpieError(f"magpie GET {path.split('?')[0]}: HTTP {exc.code}") from exc
    except (urllib.error.URLError, OSError) as exc:
        reason = getattr(exc, "reason", exc)
        raise MagpieError(f"magpie GET {path.split('?')[0]}: not reachable ({type(reason).__name__})") from exc
    try:
        return json.loads(body)
    except ValueError as exc:
        raise MagpieError(f"magpie GET {path.split('?')[0]} answered no JSON") from exc


def _number(value) -> float | int | None:
    return value if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def _rows(data, name: str) -> list[dict]:
    rows = data.get(name) if isinstance(data, dict) else None
    return [row for row in rows if isinstance(row, dict)] if isinstance(rows, list) else []


def _sum(values: list) -> int | float | None:
    return None if any(value is None for value in values) else sum(values)


def _totals(row: dict) -> dict:
    """magpie's totals as a usage row; a count magpie does not give is None."""
    calls, unpriced, cost = _number(row.get("calls")), _number(row.get("unpriced")), _number(row.get("cost"))
    tokens = {name: _number(row.get(field)) for name, field in (("input", "input"), ("output", "output"),
                                                                ("cacheRead", "cache_read"))}
    total = _sum([tokens["input"], tokens["output"], tokens["cacheRead"], _number(row.get("cache_write"))])
    priced_none = calls is not None and calls > 0 and unpriced == calls
    return {"requests": calls, "tokens": {"total": total, **tokens}, "costUsd": None if priced_none else cost}


def _added(rows: list[dict]) -> dict:
    return {"requests": _sum([row["requests"] for row in rows]),
            "tokens": {name: _sum([row["tokens"][name] for row in rows])
                       for name in ("total", "input", "output", "cacheRead")},
            "costUsd": _sum([row["costUsd"] for row in rows])}


def _account_label(row: dict) -> str | None:
    """The account as magpie names it; a name that is an address is shown as a hash, as the quota view does."""
    name = row.get("name") or row.get("account")
    if not isinstance(name, str) or not name:
        return None
    return "#" + hashlib.sha256(name.encode()).hexdigest()[:12] if "@" in name else name


def _machine(row: dict) -> str | None:
    name = row.get("callerKeyName") or row.get("name") or row.get("callerKeyId")
    return name if isinstance(name, str) and name else None


def usage_view(data, range_: str) -> dict:
    """``GET /v1/magpie/usage``'s summary by model, provider, account and caller key (``byMachine``)."""
    if not isinstance(data, dict):
        raise MagpieError("magpie's usage is not an object")
    models = [{"model": row.get("model"), "provider": row.get("provider"), **_totals(row)} for row in _rows(data, "models")]
    providers: dict = {}
    for row in models:
        providers.setdefault(row["provider"], []).append(row)
    machines = [{"machine": _machine(row), **_totals(row)} for row in _rows(data, "callerKeys") if _machine(row)]
    return {"range": range_, "generatedAt": None, **_totals(data), "byModel": models,
            "byProvider": [{"provider": name, **_added(rows)} for name, rows in providers.items()],
            "byAccount": [{"account": _account_label(row), **_totals(row)} for row in _rows(data, "accounts")],
            "byMachine": sorted(machines, key=lambda row: row["machine"])}


def usage(proxy: proxies.Proxy, range_: str) -> dict:
    if range_ not in PERIODS:
        raise ValueError(f"range must be one of {', '.join(PERIODS)}")
    return usage_view(get(proxy, f"/v1/magpie/usage?period={range_}"), range_)


def model_rows(data, remote_id: str = "") -> list[dict]:
    """``/v1/models``' list as ``provider``, ``id`` (``<provider>/<model>``, without the node's ``<remote_id>/``),
    ``contextWindow`` and ``disabled`` rows; ``raw`` keeps magpie's own id."""
    prefix = f"{remote_id}/" if remote_id else ""
    rows = []
    for item in _rows(data, "data"):
        raw = item.get("id")
        if not isinstance(raw, str) or not raw:
            continue
        name = raw[len(prefix):] if prefix and raw.startswith(prefix) else raw
        provider = name.partition("/")[0] if "/" in name else ""
        context = _number(item.get("context_length")) or _number(item.get("context_window"))
        rows.append({"provider": provider, "id": name, "raw": raw, "contextWindow": context, "disabled": False})
    return rows


def _window(window: dict) -> tuple[str, float | None, object] | None:
    """(name, used percent, reset) of a window that is a share; None for a balance, a text or an unlimited one."""
    name = window.get("name")
    if not isinstance(name, str) or not name or window.get("text") or window.get("unlimited") \
            or window.get("balance") is not None:
        return None
    used, remaining = _number(window.get("used")), _number(window.get("remaining"))
    percent = used if used is not None else (100 - remaining if remaining is not None else None)
    return name, percent, window.get("resetsAt")


def account(card: dict) -> dict:
    """A magpie quota card as an account the quota view reads: its windows in the account's quota fields
    (``5 hours`` five-hour, ``7 days`` weekly, ``30 days`` monthly, any other a custom window), a window whose share
    magpie does not give left out (unknown), and ``quotaUnavailable`` when no window is known."""
    quota: dict = {}
    custom = []
    for window in card.get("windows") if isinstance(card.get("windows"), list) else []:
        read = _window(window) if isinstance(window, dict) else None
        if read is None or read[1] is None:
            continue
        name, percent, reset = read
        fields = WINDOW_FIELDS.get(name.strip().lower())
        if fields and fields[0] not in quota:
            quota[fields[0]], quota[fields[1]] = percent, reset
        else:
            custom.append({"label": name, "percent": percent, "resetAt": reset})
    if custom:
        quota["customWindows"] = custom
    if not quota:
        quota["quotaUnavailable"] = True
    # a hash, so an address used as an account name never leaves the host
    ident = "#" + hashlib.sha256(f"magpie:{card.get('provider')}:{card.get('name')}".encode()).hexdigest()[:12]
    return {"provider": card.get("provider"), "id": ident,
            "label": card.get("plan") or card.get("kind"), "plan": card.get("plan") or "",
            "health": "error" if card.get("error") else "deferred" if card.get("readDeferred") else "ok",
            "active": False, "paused": False, "quota": quota}


def accounts(data, *, balances: bool = False) -> list[dict]:
    """The subscription and plan cards of ``/v1/magpie/quotas`` as accounts. A key's balance has no window, so the
    quota view leaves it out; with ``balances`` it stays, as an account whose headroom is unknown."""
    return [account(card) for card in _rows(data, "data") if balances or card.get("kind") != "balance"]


def quota_accounts(proxy: proxies.Proxy) -> list[dict]:
    return accounts(get(proxy, "/v1/magpie/quotas?kept=1"))


def model_accounts(proxy: proxies.Proxy) -> list[dict]:
    """The accounts a model can run on, a key's balance included: the settings check must not call a model
    of a balance-backed provider ``no_account``."""
    return accounts(get(proxy, "/v1/magpie/quotas?kept=1"), balances=True)


def view(proxy: proxies.Proxy, name: str):
    """One ``lina models`` view of magpie, in the shape the renderers read."""
    if name == "list":
        return model_rows(get(proxy, "/v1/models"), proxy.remote_id)
    if name in ("accounts", "quota"):
        return {"accounts": quota_accounts(proxy), "notes": []}
    if name == "status":
        rows = model_rows(get(proxy, "/v1/models"), proxy.remote_id)
        port = urlsplit(base_url(proxy)).port
        return {"proxy": {"running": True, "pid": None, "health": {"message": f"magpie: {len(rows)} models"}},
                "listen": {"port": port}}
    data = usage(proxy, "30d")
    return {"range": "30d", "summary": {"requests": data["requests"], "totalTokens": data["tokens"]["total"],
                                        "estimatedCostUsd": data["costUsd"]}}


def models_state(proxy: proxies.Proxy) -> dict:
    """``GET /api/lina/models/state`` from magpie: its models are on; magpie keeps no per-machine link state."""
    rows = model_rows(get(proxy, "/v1/models"), proxy.remote_id)
    return {"enabled": len(rows), "disabled": 0,
            "enabledModels": [{"provider": row["provider"] or None, "model": row["id"]} for row in rows],
            "machines": []}
