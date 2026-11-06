"""Continuations preserve recorded access unless explicitly narrowed."""

from hermes_cli.lina_work_settings import ACCESS
from hermes_cli.worker.runners.base import Handle, RunnerError


def recorded_access(handle: Handle, requested: str | None = None) -> str:
    """Refuse unrecorded sessions and escalation before delivering more work."""
    recorded = handle.data.get("access")
    if recorded not in ACCESS:
        raise RunnerError("session has no valid recorded access")
    if requested is not None and (requested not in ACCESS or ACCESS.index(requested) > ACCESS.index(recorded)):
        raise RunnerError(f"session access is {recorded}; cannot change access to {requested}")
    return recorded if requested is None else requested
