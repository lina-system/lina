"""An installed wheel carries the store's files (lina#145): ``lina store install`` runs the migrations of
``services/store/migrations`` and starts ``services/store/compose.yaml`` with its ``initdb/``, and an install
has no source tree beside it to find them in."""

import json
import subprocess
import sys
import zipfile
from pathlib import Path

from hermes_cli.store_migrations import migration_files
from tests.test_packaging_work_data import _build_wheel

PROJECT_ROOT = Path(__file__).resolve().parents[1]

# Runs on the unpacked wheel alone: -I -S drop the checkout, its editable install and site-packages.
_PROBE = """
import json, sys
sys.path.insert(0, sys.argv[1])
from hermes_cli import store, store_migrations
print(json.dumps({"migrations": [str(p) for p in store_migrations.migration_files()],
                  "compose": str(store.COMPOSE_FILE), "compose_exists": store.COMPOSE_FILE.is_file(),
                  "initdb": sorted(p.name for p in (store.COMPOSE_FILE.parent / "initdb").glob("*"))}))
"""


def test_installed_wheel_finds_every_store_migration(tmp_path):
    site = tmp_path / "site"
    with zipfile.ZipFile(_build_wheel(tmp_path)) as wheel:
        wheel.extractall(site)
    result = subprocess.run([sys.executable, "-I", "-S", "-c", _PROBE, str(site)], cwd=tmp_path, text=True,
                            encoding="utf-8", capture_output=True, timeout=120, check=False)
    assert result.returncode == 0, result.stderr[-2000:]
    installed = json.loads(result.stdout)

    source = [p.name for p in migration_files()]
    assert source and all(name[:4].isdigit() for name in source)
    assert [Path(p).name for p in installed["migrations"]] == source
    assert all(Path(p).is_relative_to(site) for p in installed["migrations"])
    assert installed["compose_exists"] and Path(installed["compose"]).is_relative_to(site)
    assert installed["initdb"] == sorted(p.name for p in (PROJECT_ROOT / "services/store/initdb").glob("*"))
