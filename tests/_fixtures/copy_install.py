"""A venv-shaped LINA install and an old wrapper that a login profile puts first, for a migrated copy's
terminal (lina#719)."""

from __future__ import annotations

import os
import sys
from pathlib import Path


def venv_install(root: Path) -> Path:
    """The interpreter and its ``lina``/``hermes`` console scripts in one bin dir, as a venv lays them out.
    Its site-packages reaches this process's import path through one ``.pth`` file."""
    bin_dir = root / "bin"
    bin_dir.mkdir(parents=True)
    (bin_dir / "python").symlink_to(sys._base_executable)
    (root / "pyvenv.cfg").write_text(f"home = {Path(sys._base_executable).parent}\n", encoding="utf-8")
    site = root / "lib" / f"python{sys.version_info.major}.{sys.version_info.minor}" / "site-packages"
    site.mkdir(parents=True)
    (site / "parent.pth").write_text("\n".join(p for p in sys.path if p and os.path.isdir(p)) + "\n",
                                     encoding="utf-8")
    for name in ("lina", "hermes"):
        script = bin_dir / name
        script.write_text(f"#!/bin/sh\nexec '{bin_dir / 'python'}' -c "
                          "'from hermes_cli.main import main; main()' \"$@\"\n", encoding="utf-8")
        script.chmod(0o755)
    return bin_dir


def old_wrapper(directory: Path) -> Path:
    """``lina`` and ``hermes`` of another install and of an old wrapper that ssh'd to the previous machine."""
    directory.mkdir(parents=True)
    for name in ("lina", "hermes"):
        script = directory / name
        script.write_text("#!/bin/sh\necho old wrapper\nexit 1\n", encoding="utf-8")
        script.chmod(0o755)
    return directory


def login_profile_puts_first(home: Path, directory: Path) -> None:
    """A ``~/.bash_profile`` in *home* that a login shell runs, putting *directory* first on PATH."""
    home.mkdir(parents=True, exist_ok=True)
    (home / ".bash_profile").write_text(f'PATH="{directory}:$PATH"\nexport PATH\n', encoding="utf-8")


def terminal_resolves(cwd: Path, *names: str) -> list[str]:
    """``command -v`` of *names* in a real local terminal (login-shell snapshot, then the command)."""
    from tools.environments.local import LocalEnvironment

    env = LocalEnvironment(cwd=str(cwd), timeout=30, env={})
    try:
        return env.execute("command -v " + " ".join(names))["output"].split()
    finally:
        env.cleanup()
