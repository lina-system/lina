"""Which runner the work settings pick for an issue (lina#256): ``worker.routing`` in order, the first rule whose
``when`` holds and whose runner is on wins, else ``worker.default_runner`` (omo when unset).

A rule's ``when`` holds when every condition it names does: ``runner_label`` is one of the issue's labels (the label
the shared judge in ``lina-system/.github`` sets), ``labels_any`` shares a label with the issue, ``size`` lists the
issue's size. A rule with no condition matches every issue (validation keeps it last).
"""

from __future__ import annotations

from hermes_cli import lina_work_settings as ws


def _holds(when: dict, labels: set[str], size: str | None) -> bool:
    if when.get("runner_label") and when["runner_label"] not in labels:
        return False
    if when.get("labels_any") and not labels.intersection(when["labels_any"]):
        return False
    return not when.get("size") or size in when["size"]


def route(doc: dict, labels: list[str] | None = None, size: str | None = None) -> dict:
    """``{runner, rule, matched}``: the runner for an issue with *labels* and *size*, the rule that chose it (None
    for the default)."""
    table = ws.runner_table(doc)
    found = set(labels or [])
    for rule in ws.get_path(doc, "worker.routing") or []:
        if not isinstance(rule, dict) or table.get(rule.get("runner"), {}).get("on") is False:
            continue
        if rule.get("runner") in table and _holds(rule.get("when") or {}, found, size):
            return {"runner": rule["runner"], "rule": rule.get("name"), "matched": True}
    return {"runner": ws.default_runner(doc), "rule": None, "matched": False}
