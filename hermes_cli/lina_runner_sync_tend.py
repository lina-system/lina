"""The daily runner sync's care of each followed block after the move and the follow (lina#531), on the hub first:

1. **model list**: when OCX's model list or the hub's version of the runner changed since the last refresh
   (``lina_runner_models.fingerprint``, kept in ``runner-sync/models.json``), the block's list is refreshed on the hub,
   and the nodes refresh theirs the same day;
2. **route**: the block's route source is checked on the hub; when it differs it is applied there and the hub probes
   the runner once with a real task before any node applies it. A check that fails, an apply that fails or a probe
   that fails after the apply holds the runner (``kind: route``), as any other failure does (#238);
3. **probe**: the hub probes the runner (unless step 2 just did), then every reachable node that runs it is asked
   through ``runner-probe`` to refresh and apply what the hub did and to probe it. A node with a run holding a slot,
   or without the verb yet, is probed on a later day.

Every probe result lands in the device's ``runner-probes.json``, which readiness shows (``lina_runner_probe``).

Before all of it, a block the hub's settings declare (``worker.runners.<name>.install``) that the hub has no copy of
yet is installed at its pin (``install_declared``): adding a tool is one block of settings, and the daily run installs,
moves, probes and lists it. A built-in block the hub lacks stays ``hub prepare needed`` (#238): nobody asked for it.
"""

from __future__ import annotations

import json

from hermes_cli import lina_ocx, lina_runner_models, lina_runner_probe, lina_runner_routes, lina_runners
from hermes_cli import lina_runner_sync_state as state

MODELS = "models.json"
NODE_TIMEOUT_S = 900


def _fingerprints_path(run):
    return state.sync_dir(run.root) / MODELS


def probe(run, pin: lina_runners.RunnerPin) -> dict:
    env = run.env
    return lina_runner_probe.probe(run.root, pin, run=env.run, popen=env.popen, ocx_logs=env.ocx_logs,
                                   clock=env.clock)


def models(run, name: str, pin: lina_runners.RunnerPin, rec: dict) -> bool:
    """Refresh the hub's model list when OCX's list or the runner's version moved; True when it was refreshed."""
    if lina_runner_models.ocx_client(pin) is None:
        return False
    if lina_ocx.external(run.root) is not None:
        rec["models"] = {"state": "external", "reason": "ocx.external is set: the operator's OCX writes the model list"}
        return False
    try:
        listed = run.env.ocx_models(run.root)
    except lina_runners.OcxError as exc:
        rec["models"] = {"state": "unknown", "reason": f"OCX's model list could not be read: {exc}"}
        return False
    mark = lina_runner_models.fingerprint(listed, lina_runners.current_version(run.root, pin))
    marks = state.read_json(_fingerprints_path(run), {})
    if marks.get(name) == mark:
        rec["models"] = {"state": "same"}
        return False
    problem = lina_runner_models.refresh(run.root, pin, run.env.run)
    if problem:
        rec["models"] = {"state": "failed", "reason": problem}
        run.event(f"{name} hub: model list not refreshed: {problem}")
        return False
    state.write_json(_fingerprints_path(run), {**marks, name: mark})
    rec["models"] = {"state": "refreshed"}
    return True


def route(run, name: str, pin: lina_runners.RunnerPin, rec: dict) -> tuple[dict | None, bool]:
    """The route step on the hub: (the hub's probe when it ran one, whether the nodes apply the route too)."""
    checked = lina_runner_routes.check(run.root, pin, run=run.env.run)
    if checked is None:
        return None, False
    code, output = checked
    if code == lina_runner_routes.IN_STEP:
        rec["route"] = {"state": "in step"}
        return None, False
    if code != lina_runner_routes.CHANGES:
        last = (output.strip().splitlines() or ["no output"])[-1][:160]
        rec["route"] = {"state": "failed", "reason": f"check exit {code}: {last}"}
        run.hold(name, "hub", pin.version, f"route check exit {code}: {last}", "route")
        return None, False
    applied = lina_runner_routes.apply(run.root, pin, run=run.env.run)
    if applied != 0:
        rec["route"] = {"state": "failed", "reason": f"apply exit {applied}"}
        run.hold(name, "hub", pin.version, f"route apply exit {applied}", "route")
        return None, False
    probed = probe(run, pin)
    if probed["state"] != "done":
        rec["route"] = {"state": "failed", "reason": f"probe after apply: {probed['why']}"}
        run.hold(name, "hub", pin.version, f"route applied but the probe failed: {probed['why']}", "route")
        return probed, False
    rec["route"] = {"state": "applied"}
    run.event(f"{name} hub: route applied after a passing probe", alert=False)
    return probed, True


def nodes(run, name: str, asks: dict, rec: dict) -> None:
    results = rec.setdefault("probes", {})
    payload = json.dumps({"runners": {name: asks}}).encode()
    for node in run.nodes:
        row = node.rows.get(name) if node.reachable else None
        if row is None or row.get("installed") is None:
            continue
        if lina_runner_probe.VERB not in node.verbs:
            results[node.name] = {"state": "update", "why": f"the node has no `{lina_runner_probe.VERB}` verb yet"}
            continue
        if node.running:
            results[node.name] = {"state": "busy", "why": f"{node.running} run(s) on the node; next day"}
            continue
        reply = node.call([lina_runner_probe.VERB], payload, NODE_TIMEOUT_S)
        got = (reply.json().get("results") or {}).get(name) or \
            {"state": "failed", "why": reply.json().get("reason") or (reply.err or "").strip()[-200:]
             or f"exit {reply.code}"}
        results[node.name] = got
        if got.get("state") == "failed":
            run.event(f"{name} on {node.name}: probe failed: {got.get('why')}")


def install_declared(run, name: str, pin: lina_runners.RunnerPin, rec: dict) -> bool:
    """Install a settings-declared block the hub has no copy of; False when that failed (the runner is held)."""
    if run.plan or name not in lina_runners.config_blocks(run.root) or \
            lina_runners.current_version(run.root, pin) is not None:
        return True
    said: list[str] = []
    try:
        lina_runners.install_pin(run.root, pin, fetch=run.env.fetch, run=run.env.run, out=said.append)
    except lina_runners.OcxError as exc:
        rec["install"] = {"state": "failed", "reason": str(exc)}
        run.hold(name, "hub", pin.version, f"install: {exc}", "install")
        return False
    rec["install"] = {"state": "installed", "version": pin.version}
    run.event(f"{name} hub {pin.version}: installed from its settings block", alert=False)
    return True


def tend(run, name: str, pin: lina_runners.RunnerPin, rec: dict) -> None:
    """Steps 1-3 for one runner; nothing on a plan, and nothing for a runner the hub has no copy of."""
    if run.plan or lina_runners.current_version(run.root, pin) is None:
        return
    asks = {"refresh": True} if models(run, name, pin, rec) else {}
    hub_probe, apply_route = route(run, name, pin, rec)
    if state.held(run.root, name, "hub"):
        rec["probes"] = {"hub": hub_probe}
        return
    if apply_route:
        asks["route"] = True
    hub_probe = hub_probe or probe(run, pin)
    rec["probes"] = {"hub": hub_probe}
    if hub_probe["state"] != "done":
        run.event(f"{name} hub: probe failed: {hub_probe['why']}")
    nodes(run, name, asks, rec)
