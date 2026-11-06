"""Which runner the work settings pick for an issue (lina#256): ``worker.routing`` in order, the first rule whose
``when`` holds and whose runner is on wins, else ``worker.default_runner`` (omo when unset).

A rule's ``when`` holds when every condition it names does: ``runner_label`` is one of the issue's labels (the label
the shared judge in ``lina-system/.github`` sets), ``labels_any`` shares a label with the issue, ``size`` lists the
issue's size. A rule with no condition matches every issue (validation keeps it last).
"""

from __future__ import annotations

from hermes_cli import lina_work_settings as ws


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
