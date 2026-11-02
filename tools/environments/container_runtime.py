"""Container runtime detection shared by service management and terminal providers."""

import logging
import os
import shutil
from typing import Optional

logger = logging.getLogger(__name__)

# Docker Desktop install paths checked when 'docker' is not in PATH
# (macOS Intel / Apple Silicon Homebrew / app bundle).
_DOCKER_SEARCH_PATHS = [
    "/usr/local/bin/docker", "/opt/homebrew/bin/docker", "/Applications/Docker.app/Contents/Resources/bin/docker",
]

_docker_executable: Optional[str] = None  # resolved once, cached

def _is_executable(path: str) -> bool:
    return os.path.isfile(path) and os.access(path, os.X_OK)



def find_docker() -> Optional[str]:
    """Locate the docker/podman CLI (cached): ``HERMES_DOCKER_BINARY`` override, ``docker``
    on PATH, ``podman`` on PATH, then macOS Docker Desktop locations; ``None`` if absent."""
    global _docker_executable
    if _docker_executable is not None:
        return _docker_executable

    override = os.getenv("HERMES_DOCKER_BINARY")
    if override and _is_executable(override):
        logger.info("Using HERMES_DOCKER_BINARY override: %s", override)
        found = override
    elif found := shutil.which("docker"):
        pass
    elif found := shutil.which("podman"):
        logger.info("Using podman as container runtime: %s", found)
    elif found := next((p for p in _DOCKER_SEARCH_PATHS if _is_executable(p)), None):
        logger.info("Found docker at non-PATH location: %s", found)
    if found:
        _docker_executable = found
    return found


def docker_runtime_name(executable: str) -> str:
    """User-facing runtime name (``"Podman"`` / ``"Docker"``) for the CLI at *executable*, so
    diagnostics and pickers name the runtime actually in use."""
    return "Podman" if "podman" in os.path.basename(executable).lower() else "Docker"


def docker_runtime_start_hint(executable: str) -> str:
    """How to bring the runtime at *executable* back up, for a "not reachable" message. Docker has
    a daemon to start; Podman is daemonless (outside Linux it runs inside a VM)."""
    if docker_runtime_name(executable) != "Podman":
        return "start Docker and retry"
    return "run `podman machine start` and retry"
