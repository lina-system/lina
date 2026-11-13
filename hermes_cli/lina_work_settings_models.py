"""The model list of the work settings (lina#256): ``GET /api/lina/work/settings/models`` and
``lina worker settings models``.

magpie is read only through ``lina_magpie`` (read-only GETs): its model list (``/v1/models``) and its quota cards
(``/v1/magpie/quotas?kept=1``). Nothing is ever written to magpie or to the settings from here.

- Listed: every model magpie lists, as ``<provider>/<id>`` (a node's ``<remote_id>/`` left off).
- Accounts are joined by ``provider``; paused accounts and accounts waiting to sign in again are left out.
- An account's headroom for a model = 100 - the highest used percent among its windows (the percent is the USED
  share); a ``customWindows`` entry with a ``modelId`` counts only for that model.
- A model's ``left`` = its best account's headroom, ``resetAt`` = the earliest reset among the windows that bind that
  account (the ones at its highest percent).
- ``state``: ``ok``; ``off`` (not listed); ``out_of_quota`` (every account with quota data is at 0 headroom);
  ``no_account`` (no usable account); ``unknown`` (no quota data, or magpie could not be read).

Every good read also writes the last good list, cut to what the check uses, to
``<work settings root>/work-settings/models-last-good.json`` with its read time (written whole: a temp file in the same
folder, then a rename). A submit runs in a new process, so ``last_good`` falls back to that file (lina#283); no
account name, email, id or label is kept in it.

Per runner, per mode default and for the fallback the view says whether its model is usable now (``ok`` or
``unknown``), with one Korean warning line when it is not. When magpie cannot be read, the last good read (kept in
memory up to ten minutes) is served with ``errors: [{source: magpie, message}]``.
"""

from __future__ import annotations

import json
import logging
import os
import tempfile
import threading
import time
from pathlib import Path

from hermes_cli import lina_work_settings as ws
from hermes_cli.lina_board_quota import WINDOWS, reset_time

LAST_GOOD_S = 600
USABLE = ("ok", "unknown")
EXTRA_WINDOWS = (("short", "shortPercent", "shortResetAt"),)
WARN = {"off": "{who} 모델이 꺼져 있습니다", "out_of_quota": "{who} 모델의 쿼터가 다 찼습니다",
        "no_account": "{who} 모델을 쓸 계정이 없습니다"}
SNAPSHOT = "models-last-good.json"
ROW_KEYS = ("provider", "id")
ACCOUNT_KEYS = ("provider", "paused", "needsReauth")
CUSTOM_KEYS = ("percent", "resetAt", "modelId")
_LAST: dict[str, tuple[float, list, list]] = {}
_LOCK = threading.Lock()
log = logging.getLogger(__name__)


def forget() -> None:
    """Drop the kept reads (tests)."""
    with _LOCK:
        _LAST.clear()


def _number(value) -> float | None:
    return float(value) if isinstance(value, (int, float)) and not isinstance(value, bool) else None


def read(now: float) -> tuple[list, list, list[dict]]:
    """``(rows, accounts, errors)``: magpie now, else the last good read of the last ten minutes, else nothing.
    magpie is found the way ``lina models`` finds it (the LINA root's ``worker.proxy``), not at the settings' root."""
    from hermes_cli import lina_magpie
    from hermes_cli.lina_models import node_proxy
    key = "magpie"
    try:
        proxy = node_proxy()
        listed = lina_magpie.model_rows(lina_magpie.get(proxy, "/v1/models"), proxy.remote_id)
        accounts = lina_magpie.model_accounts(proxy)
    except lina_magpie.MagpieError as exc:
        log.warning("work settings: magpie could not be read: %s", exc)
        with _LOCK:
            hit = _LAST.get(key)
        error = [{"source": "magpie", "message": str(exc)}]
        if hit and now - hit[0] <= LAST_GOOD_S:
            return hit[1], hit[2], error
        return [], [], error
    rows = [{"provider": row["provider"], "id": row["id"].partition("/")[2]} for row in listed if row["provider"]]
    with _LOCK:
        _LAST[key] = (now, rows, accounts)
    _write_snapshot(now, rows, accounts)
    return rows, accounts, []


def snapshot_path() -> Path:
    return ws.base() / "work-settings" / SNAPSHOT


def _account(account: dict) -> dict:
    out = {k: account[k] for k in ACCOUNT_KEYS if k in account}
    quota = account.get("quota")
    if isinstance(quota, dict):
        kept = {k: quota[k] for _, value, reset in (*WINDOWS, *EXTRA_WINDOWS) for k in (value, reset) if k in quota}
        custom = quota.get("customWindows")
        if isinstance(custom, list):
            kept["customWindows"] = [{k: row[k] for k in CUSTOM_KEYS if k in row} for row in custom
                                     if isinstance(row, dict)]
        out["quota"] = kept
    return out


def _write_snapshot(now: float, rows: list[dict], accounts: list[dict]) -> None:
    """The last good list for other processes. A failed write is logged; the view still serves this read."""
    data = {"at": now, "rows": [{k: r[k] for k in ROW_KEYS if k in r} for r in rows],
            "accounts": [_account(a) for a in accounts]}
    path = snapshot_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        fd, tmp = tempfile.mkstemp(prefix=f".{SNAPSHOT}.", dir=path.parent)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as fh:
                json.dump(data, fh)
            os.replace(tmp, path)
        except BaseException:
            Path(tmp).unlink(missing_ok=True)
            raise
    except (OSError, TypeError, ValueError) as exc:
        log.warning("work settings: the models snapshot could not be written: %s", exc)


def _read_snapshot(now: float) -> tuple[list, list] | None:
    """The snapshot file's lists when its ``at`` is at most ten minutes old; anything else is None."""
    try:
        data = json.loads(snapshot_path().read_text(encoding="utf-8-sig"))
    except (OSError, ValueError):
        return None
    at = _number(data.get("at")) if isinstance(data, dict) else None
    rows, accounts = (data.get("rows"), data.get("accounts")) if at is not None else (None, None)
    if not isinstance(rows, list) or not isinstance(accounts, list) or not 0 <= now - at <= LAST_GOOD_S:
        return None
    return [r for r in rows if isinstance(r, dict)], [a for a in accounts if isinstance(a, dict)]


def last_good(now: float) -> tuple[list, list] | None:
    """``(live rows, accounts)`` of the last good read when it is at most ten minutes old: this process's own, else
    the snapshot file another process wrote, else None. Never calls magpie: the run path decides from this at submit
    (lina#257), so a hanging magpie never holds a submit."""
    with _LOCK:
        hit = _LAST.get("magpie")
    if hit and now - hit[0] <= LAST_GOOD_S:
        return hit[1], hit[2]
    return _read_snapshot(now)


def _windows(account: dict, model_id: str) -> list[tuple[float, float | None]]:
    quota = account.get("quota") if isinstance(account.get("quota"), dict) else {}
    out = [(_number(quota.get(value)), reset_time(quota.get(reset))) for _, value, reset in (*WINDOWS, *EXTRA_WINDOWS)
           if _number(quota.get(value)) is not None]
    custom = quota.get("customWindows")
    for row in custom if isinstance(custom, list) else []:
        if isinstance(row, dict) and _number(row.get("percent")) is not None \
                and row.get("modelId") in (None, "", model_id):
            out.append((_number(row["percent"]), reset_time(row.get("resetAt"))))
    return out


def _headroom(account: dict, model_id: str) -> tuple[float, float | None] | None:
    wins = _windows(account, model_id)
    if not wins:
        return None
    top = max(percent for percent, _ in wins)
    resets = [reset for percent, reset in wins if percent == top and reset is not None]
    return 100.0 - top, min(resets) if resets else None


def model_row(row: dict, accounts: list[dict]) -> dict:
    usable = [a for a in accounts if a.get("provider") == row.get("provider")
              and not a.get("paused") and not a.get("needsReauth")]
    heads = [h for a in usable if (h := _headroom(a, str(row.get("id")))) is not None]
    best = max(heads, key=lambda h: (h[0], -(h[1] or 0))) if heads else None
    if not usable:
        state = "no_account"
    elif best is None:
        state = "unknown"
    elif best[0] > 0:
        state = "ok"
    else:
        state = "unknown" if len(heads) < len(usable) else "out_of_quota"
    left = None if best is None else max(0.0, min(100.0, best[0]))
    return {"provider": row.get("provider"), "id": row.get("id"), "state": state,
            "left": None if left is None else (int(left) if left == int(left) else left),
            "resetAt": None if best is None else best[1], "accounts": len(usable)}


def identity(value: str | None, worker: dict) -> str | None:
    """``<provider>/<id>`` of a model value in the one identity, None when it names none."""
    ref = _resolve(value, worker)
    return None if ref is None else "/".join(ref)


def _resolve(value: str | None, worker: dict) -> tuple[str, str] | None:
    from hermes_cli.worker.models import parse
    from hermes_cli.worker.runners.base import RunnerError
    try:
        profile = parse(value or "", worker)
    except RunnerError:
        return None
    if profile is None:
        return None
    return str(profile.provider), str(profile.id)


def _state(value: str | None, worker: dict, rows: list[dict], accounts: list[dict], read_ok: bool) -> str | None:
    if not value:
        return None
    ref = _resolve(value, worker)
    if ref is None or not read_ok:
        return "unknown"
    row = next((r for r in rows if (r.get("provider"), r.get("id")) == ref), None)
    if row is None:
        return "off"
    return model_row(row, accounts)["state"]


def _text(value) -> str | None:
    """A model value read from the settings; a wrong type (a hand edit validation never saw) reads as unset."""
    return value if isinstance(value, str) and value else None


def _check(who: str, value: str | None, fallback: str | None, worker: dict, ctx: tuple) -> dict:
    value, fallback = _text(value), _text(fallback)
    state = _state(value, worker, *ctx)
    usable = state is None or state in USABLE
    if usable:
        warning = None
    elif who == "대체":
        warning = WARN[state].format(who=who) + " · 다음 실행은 원래 모델로 갑니다"
    else:
        tail = f"다음 실행은 {fallback}로 갑니다" if fallback else "대체 모델이 없습니다"
        warning = WARN[state].format(who=who) + " · " + tail
    return {"model": value, "identity": identity(value, worker), "state": state, "usable": usable,
            "fallback": fallback, "warning": warning}


def view(base: Path | None = None, now: float | None = None) -> dict:
    """The picker view: ``{models, runners, modes, fallback, warnings, errors}``."""
    from hermes_cli.lina_work_settings_store import Store
    now = time.time() if now is None else now
    store = Store(base)
    rows, accounts, errors = read(now)
    doc = store.current().doc
    worker = ws.mapping(doc.get("worker"))  # a wrong type reads as absent here; writes refuse it with its path
    models = ws.mapping(worker.get("models"))
    fallback = _text(models.get("fallback"))
    ctx = (rows, accounts, bool(rows) or not errors)
    runners = [{"name": name, **_check(name, spec.get("model"), _text(spec.get("fallback_model")) or fallback, worker, ctx)}
               for name, spec in ws.runner_table(doc).items()]
    modes = [{"mode": mode, **_check(f"{mode} 기본", value, fallback, worker, ctx)}
             for mode, value in ws.mapping(models.get("defaults")).items()]
    last = _check("대체", fallback, None, worker, ctx)
    warnings = [row["warning"] for row in (*runners, *modes, last) if row["warning"]]
    return {"models": [model_row(r, accounts) for r in rows], "runners": runners, "modes": modes,
            "fallback": last, "warnings": warnings, "errors": errors}
