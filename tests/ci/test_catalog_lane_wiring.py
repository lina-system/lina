"""Regression for #74: a catalog-only pull request must run the catalog tests.

``scripts/ci/classify_changes.py`` emits ``python=false, catalog=true`` for a
diff made only of ``plugin-catalog/*.yaml``. Before this wiring nothing in
``ci.yml`` consumed the ``catalog`` output, so such a PR ran no test at all:
the Linux slices skip and the catalog tests existed only in the upstream
``catalog-tests.yml``, which ``.github/workflows-upstream/`` keeps inactive.

These tests replay the real files: the classifier, the composite action's
outputs, ``ci.yml``'s job conditions and the catalog job's own ``run:`` step
(with ``scripts/run_tests.sh`` receipted, not executed).
"""

from __future__ import annotations

import importlib.util
import json
import re
from pathlib import Path
from typing import Any

import pytest

from tests.ci import _gha_expr as gha
from tests.ci import workflow_steps

ROOT = Path(__file__).resolve().parents[2]
CATALOG_ONLY = ["plugin-catalog/hermes-rustpush-imessage.yaml"]


def _load(rel: str) -> Any:
    path = ROOT / rel
    spec = importlib.util.spec_from_file_location(path.stem, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _yaml(rel: str) -> dict:
    yaml = pytest.importorskip("hermes_yaml")
    return yaml.safe_load((ROOT / rel).read_text(encoding="utf-8-sig"))


def _detect_outputs(files: list[str]) -> dict[str, str]:
    """Classifier lines -> the composite action's outputs -> ci.yml detect outputs."""
    raw = {k: gha.to_string(v) for k, v in _load("scripts/ci/classify_changes.py").classify(files).items()}
    action = _yaml(".github/actions/detect-changes/action.yml")
    action_out = {k: gha.render(v["value"], {"steps": {"classify": {"outputs": raw}}})
                  for k, v in action["outputs"].items()}
    detect = _yaml(".github/workflows/ci.yml")["jobs"]["detect"]
    ctx = {"steps": {"classify": {"outputs": action_out}}}
    return {k: gha.render(v, ctx) for k, v in detect["outputs"].items()}


def _jobs_that_run(files: list[str], event: str = "pull_request") -> dict[str, Any]:
    """Every ci.yml job a diff starts, with the context its conditions saw."""
    jobs = _yaml(".github/workflows/ci.yml")["jobs"]
    detect = _detect_outputs(files)
    ran: dict[str, Any] = {"detect": {"outputs": detect}}
    pending, done = list(jobs), {"detect"}
    while pending:
        progressed = False
        for name in list(pending):
            body = jobs[name]
            needs = body.get("needs") or []
            needs = [needs] if isinstance(needs, str) else list(needs)
            if any(n not in done for n in needs):
                continue
            pending.remove(name)
            done.add(name)
            progressed = True
            if name == "detect":
                continue
            all_ran = all(n in ran for n in needs)
            ctx = {
                "needs": {n: {"result": "success" if n in ran else "skipped",
                              "outputs": (ran.get(n) or {}).get("outputs", {})} for n in needs},
                "github": {"event_name": event, "ref_type": "branch", "ref": "refs/heads/dev"},
                "__status__": {"always": True, "success": all_ran, "failure": False, "cancelled": False},
            }
            used_status = isinstance(body.get("if"), str) and re.search(
                r"\b(always|success|failure|cancelled)\(", body["if"])
            if not used_status and not all_ran:
                continue
            if not gha.condition(body.get("if"), ctx):
                continue
            ran[name] = {"ctx": ctx, "body": body}
        assert progressed, f"unresolvable needs among {pending}"
    return ran


def test_catalog_only_pull_request_runs_the_catalog_lane_and_skips_the_slices():
    """The classifier keeps saying python=false; the lane that replaces it must start."""
    outputs = _detect_outputs(CATALOG_ONLY)
    assert outputs["python"] == "false"
    assert outputs["catalog"] == "true"

    ran = _jobs_that_run(CATALOG_ONLY)
    assert "catalog" in ran, "a catalog-only PR starts no catalog job (#74)"
    assert "linux" not in ran


def test_catalog_job_runs_every_test_file_the_discovery_script_names():
    """The job must run the discovered set, not a remembered list: a new catalog
    test joins by existing, and a stale list would silently stop covering one."""
    job = _jobs_that_run(CATALOG_ONLY)["catalog"]
    steps = [s for s in job["body"]["steps"] if s.get("name", "").startswith("Run the catalog tests")]
    assert len(steps) == 1, "missing/ambiguous catalog test step"

    selected = workflow_steps.selected_files(steps[0], job["ctx"], ROOT)
    expected = set(_load("scripts/ci/catalog_test_files.py").catalog_test_files(ROOT))

    assert expected, "catalog discovery found no test files"
    assert selected == expected
    assert "tests/hermes_cli/test_plugin_catalog.py" in selected
    assert "tests/scripts/test_validate_plugin_catalog.py" in selected


def test_catalog_job_failure_fails_the_required_check():
    """A catalog-only PR skips the Linux slices, so this aggregate is the only gate."""
    jobs = _yaml(".github/workflows/ci.yml")["jobs"]
    aggregate = next((j for j in jobs.values() if j.get("name") == "Python tests (Linux)"), None)
    assert aggregate is not None, "the ruleset's required check name moved"
    needs = aggregate["needs"] if isinstance(aggregate["needs"], list) else [aggregate["needs"]]
    assert "catalog" in needs

    steps = [s for s in aggregate["steps"] if s.get("name", "").startswith("Every Linux slice")]
    assert len(steps) == 1

    def verdict(catalog_result: str) -> None:
        ctx = {
            "needs": {
                "detect": {"result": "success",
                           "outputs": {"python": "false", "catalog": "true"}},
                "linux": {"result": "skipped"},
                "catalog": {"result": catalog_result},
            },
        }
        workflow_steps.outputs(steps[0], ctx)

    verdict("success")
    with pytest.raises(AssertionError):
        verdict("failure")


def test_every_detect_output_the_workflow_reads_is_declared():
    """An undeclared output resolves to "" forever and its lane never starts."""
    jobs = _yaml(".github/workflows/ci.yml")["jobs"]
    declared = set(jobs["detect"]["outputs"])
    referenced = set(re.findall(r"needs\.detect\.outputs\.(\w+)", json.dumps(jobs)))

    assert "catalog" in referenced
    assert referenced - declared == set()
