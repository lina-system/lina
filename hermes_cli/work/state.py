"""An item's state is recomputed the moment it is written: every write of a note, wait, evidence, link or close
marks the item (``touched``), and ``recompute`` (run by the CLI right after the command) evaluates the marked items
and their goals once, closes those whose own check passes and stores the result. ``work check`` is the same pass
over every open item; nothing here runs on a timer."""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor

from hermes_cli.work import ledger
from hermes_cli.work.common import Lock, iso, load_json, now, paths, save_json, warn
from hermes_cli.work.evaluate import ALERT, evaluate, item_name
from hermes_cli.work.locale import status, t
from hermes_cli.work.people import name

RECOMPUTE_OPS = {"note", "wait", "evidence", "done", "owner_done", "owner_undo", "drop", "reopen", "link", "unlink",
                 "edit", "wait_release", "wait_restore", "undo", "reply"}
_TOUCHED: dict[str, None] = {}
_SUPPRESS: list[bool] = []


def touched(item_id: str, op: str) -> None:
    if not _SUPPRESS and op in RECOMPUTE_OPS:
        _TOUCHED[item_id] = None


def recompute() -> None:
    """Evaluate what this command wrote (and the goals above it) and store the state now."""
    ids = list(_TOUCHED)
    _TOUCHED.clear()
    if not ids:
        return
    recs, _ = ledger.read_all()
    goals = [recs[i]["parent"] for i in ids if i in recs and recs[i].get("kind") == "할 일" and recs[i].get("parent")]
    ids = [i for i in dict.fromkeys(ids + goals) if i in recs and recs[i].get("state") == "open"]
    if ids:
        try:
            check(ids, quiet=True)
        except (OSError, ValueError, KeyError, LookupError) as exc:
            warn(t("바로 다시 검사 못 함({kind}) — 다음 검사 때 바뀐다", kind=type(exc).__name__))


def build_ctx(recs: dict):
    from hermes_cli.work.sources import sessions_index
    sess, sess_id = sessions_index()
    st = load_json(paths()["state"], {})
    runs = {rid: (code, reason) for rid, code, reason in
            ledger.db().execute("SELECT id, status, reason FROM v_run").fetchall()}
    ctx = {"recs": recs, "sess": sess, "sess_id": sess_id, "gh": st.get("gh_cache", {}), "prev": st.get("items", {}),
           "runs": runs}
    return ctx, st


def _evaluate_all(recs: dict, ids: list[str], ctx: dict, st: dict) -> dict:
    out = dict(st.get("items", {}))
    tier = {k: [i for i in ids if recs[i]["kind"] == k] for k in ("할 일", "목표")}
    with ThreadPoolExecutor(6) as ex:
        out.update(dict(zip(tier["할 일"], ex.map(lambda i: evaluate(recs[i], ctx), tier["할 일"]))))
        ctx["prev"] = out
        out.update(dict(zip(tier["목표"], ex.map(lambda i: evaluate(recs[i], ctx), tier["목표"]))))
    for i in ids:
        if recs[i]["kind"] in ("영역", "메모"):
            out[i] = evaluate(recs[i], ctx)
    return out


def _close_passed(ids: list[str], out: dict, dry: bool):
    """A goal or to-do closes only on its own check: one ``done`` line for each item whose check passed."""
    from hermes_cli.work.waits import release_waits
    closed = []
    _SUPPRESS.append(True)
    try:
        with Lock():
            cur, _ = ledger.read_all()
            for i in ids:
                c, r = out[i], cur.get(i)
                if (c.get("pass") and r and r.get("state") == "open" and not r.get("every")
                        and r["kind"] in ("목표", "할 일") and not dry):
                    ledger.append(r, "done", "checker", state="done", done_at=now(), done_by=t("검사"),
                                  done_evidence=(c.get("check") or {}).get("out", "")[:400])
                    cur[i] = dict(r, state="done")
                    closed.append(i)
                    c["status"], c["line"] = "done", t("검사 통과")
            released = release_waits(cur, set(closed), now(), "checker") if closed else []
    finally:
        _SUPPRESS.pop()
    return closed, released, cur


def _alerts(recs: dict, ids: list[str], out: dict, closed: list[str]):
    from hermes_cli.work.notify import due_alerts
    from hermes_cli.work.view import row_view
    alerts = []
    for i in ids:
        out[i]["checked"] = now()
        r = recs[i]
        if (out[i]["status"] in ALERT | {"evidence_wait"} and r.get("state") == "open" and not out[i].get("kid_stall")
                and not (r["kind"] == "목표" and row_view(r, out[i], recs)["health"] in ("hold", "wait", "tidy"))):
            alerts.append((r, out[i]))
    return alerts + due_alerts([recs[i] for i in ids if i not in closed])


def _stage_moves(ids: list[str], out: dict, st: dict) -> None:
    """One meta line per stage change (the record stays append only)."""
    from hermes_cli.work.view import row_view
    cur, _ = ledger.read_all()
    moves = []
    for i in ids:
        r = cur.get(i)
        if not r or r["kind"] != "목표":
            continue
        v = row_view(r, out[i], cur)
        out[i]["view"] = v
        if (st.get("stages") or {}).get(i) != v["stage"]:
            moves.append({"id": i, "op": "stage", "_meta": True, "stage": v["stage"],
                          "from": (st.get("stages") or {}).get(i), "ts": now(), "at": iso(), "writer": "checker"})
            st.setdefault("stages", {})[i] = v["stage"]
            st.setdefault("stage_at", {})[i] = now()
    if moves:
        with Lock():
            for m in moves:
                ledger.append_meta(m)


def _report(recs: dict, ids: list[str], out: dict, closed: list[str]) -> None:
    for i in ids:
        r, c = recs[i], out[i]
        ch = c.get("check") or {}
        print(f"{i:5} {name(r['by']):3} {status(c['status']):6} {item_name(r)} — {c['line']}"
              + (f"  [rc={ch.get('rc')} {ch.get('secs')}s]" if ch else ""))
    if closed:
        print(t("끝남으로 바뀜:"), " ".join(closed))


def check(ids: list[str] | None = None, *, dry: bool = False, notify: bool = False, quiet: bool = False) -> None:
    from hermes_cli.work.notify import notify as send, notify_tidy
    from hermes_cli.work.view import row_view
    from hermes_cli.work.waits import tell_released
    recs, order = ledger.read_all()
    ids = [i.upper() for i in ids] if ids else [i for i in order if recs[i].get("state") == "open"]
    ids = [i for i in ids if i in recs]
    ctx, st = build_ctx(recs)
    out = _evaluate_all(recs, ids, ctx, st)
    closed, released, cur = _close_passed(ids, out, dry)
    if not dry:
        tell_released(released, [cur[i] for i in closed])
    alerts = _alerts(recs, ids, out, closed)
    st["items"] = {k: v for k, v in out.items() if k in recs}
    if not dry:
        _stage_moves(ids, out, st)
    st["gh_cache"] = {k: v for k, v in ctx["gh"].items() if now() - v["at"] < 86400}
    st["at"] = now()
    sent = send(alerts, recs, dry=dry) if (notify or dry) else []
    if notify or dry:
        sent += notify_tidy([r for r in recs.values() if r["kind"] == "목표" and r.get("state") == "open"
                             and row_view(r, out.get(r["id"], {}), recs)["health"] == "tidy"], dry=dry)
    st["last_notify"] = sent or st.get("last_notify", [])
    if not dry:
        save_json(paths()["state"], st)
        with open(paths()["log"], "a", encoding="utf-8") as lf:  # windows-footgun: ok — an append, not a read
            lf.write(f"{iso()} check n={len(ids)} closed={closed} alert={[r['id'] for r, _ in alerts]} sent={sent}\n")
    if not quiet:
        _report(recs, ids, out, closed)
