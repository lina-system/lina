"""Which runner the work settings pick for an issue (lina#256): ``worker.routing`` in order, the first rule whose
``when`` holds and whose runner is on wins, else ``worker.default_runner`` (omo when unset).

A rule's ``when`` holds when every condition it names does: ``runner_label`` is one of the issue's labels (the label
the shared judge in ``lina-system/.github`` sets), ``labels_any`` shares a label with the issue, ``size`` lists the
issue's size. A rule with no condition matches every issue (validation keeps it last).

``decide`` is the run path's one call (lina#257): at submit it picks the runner and the model a run starts with and
says where each came from, under which settings revision, and what OCX said of the model.
"""

from __future__ import annotations

import time
from dataclasses import dataclass
from pathlib import Path

from hermes_cli import lina_work_settings as ws

CHECKED = ("ok", "off", "out_of_quota", "no_account")
FALL_BACK_ON = ("off", "out_of_quota", "no_account")


def _holds(when: dict, labels: set[str], size: str | None) -> bool:
    """A condition of a wrong type (a hand edit validation never saw) never matches; it never raises either."""
    label, any_of, sizes = when.get("runner_label"), when.get("labels_any"), when.get("size")
    if label not in (None, "") and (not isinstance(label, str) or label not in labels):
        return False
    if any_of not in (None, []) and (not isinstance(any_of, list)
                                     or not labels.intersection(x for x in any_of if isinstance(x, str))):
        return False
    return sizes in (None, []) or isinstance(sizes, list) and size in sizes


def route(doc: dict, labels: list[str] | None = None, size: str | None = None) -> dict:
    """``{runner, rule, matched}``: the runner for an issue with *labels* and *size*, the rule that chose it (None
    for the default)."""
    table = ws.runner_table(doc)
    found = set(labels or [])
    rules = ws.get_path(doc, "worker.routing")
    for rule in rules if isinstance(rules, list) else []:
        runner = rule.get("runner") if isinstance(rule, dict) else None
        if not isinstance(runner, str) or runner not in table or table[runner].get("on") is False:
            continue
        when = rule.get("when")
        if (when is None or isinstance(when, dict)) and _holds(ws.mapping(when), found, size):
            return {"runner": rule["runner"], "rule": rule.get("name"), "matched": True}
    return {"runner": ws.default_runner(doc), "rule": None, "matched": False}


@dataclass(frozen=True)
class Decision:
    runner: str
    model: object  # ModelProfile | None: None is the runner's own default
    settings: dict  # {rev, runner_source, model_source, ocx_check}
    fallback: dict | None = None  # the model_fallback receipt {from, to, why}


def _named(profile) -> str:
    return f"{profile.provider}/{profile.id}" + (f":{profile.thinking}" if profile.thinking else "")


def decide(base: Path | None, runner: str | None, mode: str, override: str | None, labels: list[str] | None = None,
           size: str | None = None, now: float | None = None) -> Decision:
    """The runner (``--runner``, else routing on *labels*/*size*, else the default; one that is off is refused with
    ``SettingsError``) and the model (``--model``, else the runner's ``model``, else the mode default). When OCX has
    that model ``off``, ``out_of_quota`` or ``no_account`` and the fallback (the runner's ``fallback_model``, else
    ``worker.models.fallback``) is usable, the fallback is used; an explicit ``--model`` is only checked. OCX is never
    called here: the check reads the models view's last good list (``last_good``: this process's, else the snapshot
    file another process wrote), and without one of the last ten minutes it is ``unchecked``, so a submit never waits
    on OCX."""
    from hermes_cli import lina_work_settings_models as wm
    from hermes_cli.lina_work_settings_store import Store
    from hermes_cli.worker import models
    from hermes_cli.worker.runners.base import RunnerError
    rev = Store(base).current()
    doc = rev.doc
    worker = ws.mapping(doc.get("worker"))
    if runner:
        runner_source = "--runner"
    else:
        hit = route(doc, labels, size)
        runner, runner_source = hit["runner"], f"routing:{hit['rule']}" if hit["matched"] else "default"
    spec = ws.mapping(ws.runner_table(doc).get(runner))
    if spec.get("on") is False:
        raise ws.SettingsError([{"path": f"worker.runners.{runner}.on", "message": f"runner {runner!r} is off"}])
    own = wm._text(spec.get("model"))
    if override:
        value, model = override, models.parse(override, worker)
    elif own:
        value, model = own, models.parse(own, worker, source=f"runner:{runner}")
    else:
        value = wm._text(ws.mapping(ws.mapping(worker.get("models")).get("defaults")).get(mode))
        model = models.for_run(mode, None, worker)
    check, fallback = "unchecked", None
    snapshot = wm.last_good(time.time() if now is None else now) if model is not None else None
    if snapshot is not None:
        ctx = (*snapshot, True)
        state = wm._state(value, worker, *ctx)
        check = state if state in CHECKED else "unchecked"
        other = wm._text(spec.get("fallback_model")) or wm._text(ws.mapping(worker.get("models")).get("fallback"))
        if check in FALL_BACK_ON and not override and other and wm._state(other, worker, *ctx) in wm.USABLE:
            try:
                used = models.parse(other, worker, source=f"fallback:{check}")
            except RunnerError:
                used = None
            if used is not None:
                fallback = {"from": _named(model), "to": _named(used), "why": check}
                model = used
    settings = {"rev": rev.id, "runner_source": runner_source,
                "model_source": model.source if model is not None else "runner", "ocx_check": check}
    return Decision(runner, model, settings, fallback)
