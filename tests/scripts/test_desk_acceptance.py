"""scripts/desk_acceptance.py passes on this checkout and fails when the install makes no Desk.

Acceptance for lina-system/lina#44.
"""

import importlib.util
import json
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "desk_acceptance.py"


def _load():
    spec = importlib.util.spec_from_file_location("desk_acceptance", SCRIPT)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def test_fresh_install_passes_every_check(tmp_path):
    out = tmp_path / "results.json"

    assert _load().main(["--json", str(out)]) == 0
    assert [row["check"] for row in json.loads(out.read_text()) if row["passed"]] == [
        "install", "layout", "agents", "workdir", "shared", "api"]


def test_an_install_without_a_desk_fails(tmp_path):
    out = tmp_path / "results.json"
    no_desk = f"{sys.executable} -c pass"

    assert _load().main(["--command", no_desk, "--json", str(out)]) == 1
    rows = {row["check"]: row["passed"] for row in json.loads(out.read_text())}
    assert rows["install"] is True and rows["layout"] is False
