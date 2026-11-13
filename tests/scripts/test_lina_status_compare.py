"""``scripts/lina_status_compare.py`` (lina#662): the old board's machine and quota HTML beside the core's replies."""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "lina_status_compare.py"


def _load():
    spec = importlib.util.spec_from_file_location("lina_status_compare", SCRIPT)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


def _row(name: str, cpu: str, mem: str, disk: str) -> str:
    """One machine row as the old board draws it (``board/views/machines.py``)."""
    gauge = '<span class="gauge {k}"><span class="sr">x</span><i class="bar" style="--p:1%"></i><b>{v}</b></span>'
    return (f'<details class="task machine" data-key="m-{name}"><summary><span class="t"><span class="name">{name}'
            f'</span></span>' + "".join(gauge.format(k=k, v=v) for k, v in (("cpu", cpu), ("mem", mem), ("disk", disk)))
            + "</summary><h4>정보</h4></details>")


def _write(tmp_path: Path, name: str, data: dict[str, object]) -> str:
    path = tmp_path / name
    path.write_text(json.dumps(data, ensure_ascii=False), encoding="utf-8")
    return str(path)


def test_machines_and_quota_print_side_by_side_and_a_difference_fails(tmp_path, capsys):
    old: dict = {"machines": _row("lina-hub", "23%", "46%", "19%") + _row("bots-linux", "5%", "30%", "70%")
           + _row("studio-mac", "—", "—", "—"),
           "quota": '<div class="qhead"><b>Claude</b><span>50% 사용</span></div>'
                    '<div class="qhead"><b>Antigravity</b><span>48% 사용</span></div>'}
    devices: dict = {"machines": [{"name": "lina-hub", "role": "work", "cpu": 23.4, "memory": 46.0, "disk": 19.2,
                             "metricsWhy": None}],
               "monitored": [{"name": "studio-mac", "role": "monitored", "cpu": None, "memory": None, "disk": None,
                              "metricsWhy": "down"}],
               "person": [{"name": "work-mac", "role": "person"}]}
    quota = {"quota": [{"service": "Claude", "percent": 49.4}, {"service": "Antigravity", "percent": 47.7}]}
    args = ["--old", _write(tmp_path, "old.json", old), "--devices", _write(tmp_path, "devices.json", devices),
            "--quota", _write(tmp_path, "quota.json", quota)]

    assert _load().main(args) == 1  # bots-linux is on the old board only
    lines = {line.split()[0]: line.split() for line in capsys.readouterr().out.splitlines() if line.strip()}
    assert lines["lina-hub"][-1] == "same" and lines["lina-hub"][2:4] == ["23/46/19", "23.4/46/19.2"]
    assert lines["studio-mac"][-2:] == ["down", "same"]
    assert lines["bots-linux"][-2:] == ["old", "only"] and lines["work-mac"][-2:] == ["new", "only"]
    assert lines["Claude"][1:] == ["50", "49.4", "same"] and lines["Antigravity"][1:] == ["48", "47.7", "same"]

    devices["machines"][0]["cpu"] = 40.0
    devices["monitored"].append({"name": "bots-linux", "role": "monitored", "cpu": 5.0, "memory": 30.0, "disk": 70.0})
    args[3] = _write(tmp_path, "devices.json", devices)
    assert _load().main(args) == 1
    assert "differs" in next(line for line in capsys.readouterr().out.splitlines() if line.startswith("lina-hub"))
    devices["machines"][0]["cpu"] = 23.0
    args[3] = _write(tmp_path, "devices.json", devices)
    assert _load().main(args) == 0
