"""Execution-environment backends for the terminal tool: per-backend builders, config-to-kwargs
shapers, and requirement checkers, routed by dispatch/spec tables. Split out of ``tools/terminal_tool.py``;
the terminal tool imports the builders/checkers it uses from here."""

import functools
import importlib.util
import logging
import subprocess
from typing import Any, Dict, Optional

from tools.environments.local import LocalEnvironment as _LocalEnvironment
from tools.environments.ssh import SSHEnvironment as _SSHEnvironment
from tools.terminal_tool_config import _get_plugin_env_provider

# Log-record parity with the origin module.
logger = logging.getLogger("tools.terminal_tool")

# Human reason for the most recent failed requirements check (None after a passing one). The CLI
# startup notice and `hermes doctor` read it through terminal_backend_unavailable_reason() so the user
# hears WHY the terminal tool is missing instead of discovering it on first use.
_last_unavailable_reason: Optional[str] = None


def _reject(reason: str) -> bool:
    """Log *reason*, remember it for the user-facing notice, and return False for the checker."""
    global _last_unavailable_reason
    _last_unavailable_reason = reason
    logger.error(reason)
    return False


def _record_unavailable_reason(reason: Optional[str]) -> None:
    """Store (or clear) the last failure reason without logging; used by check_terminal_requirements."""
    global _last_unavailable_reason
    _last_unavailable_reason = reason


def terminal_backend_unavailable_reason() -> Optional[str]:
    """Why the last terminal requirements check failed, in plain words; None when it passed."""
    return _last_unavailable_reason


_VERCEL_SANDBOX_DEFAULT_CWD = "/vercel/sandbox"
_SUPPORTED_VERCEL_RUNTIMES = ("node24", "node22", "python3.13")
_BUILTIN_BACKENDS = "local, ssh"

# Config -> kwargs shapers, driven by (out_key, config_key, default) tables. The container table's
# (key, default) literal is intentionally greppable; tools/terminal_tool.py keeps its own for the AST test.
_SSH_KEYS = (("host", "ssh_host", ""), ("user", "ssh_user", ""), ("port", "ssh_port", 22),
             ("key", "ssh_key", ""), ("persistent", "ssh_persistent", True))
_RESOURCE_KEYS = (("cpu", "container_cpu", 1), ("memory", "container_memory", 5120),
                  ("disk", "container_disk", 51200), ("persistent_filesystem", "container_persistent", True))
_CONTAINER_KEYS = (
    ("container_cpu", 1), ("container_memory", 5120), ("container_disk", 51200),
    ("container_persistent", True), ("modal_mode", "auto"), ("vercel_runtime", ""), ("vercel_image", ""),
    ("docker_volumes", []), ("docker_mount_cwd_to_workspace", False), ("docker_forward_env", []),
    ("docker_env", {}), ("docker_run_as_host_user", False), ("docker_extra_args", []),
    ("docker_shm_size", "1g"), ("docker_network", True), ("docker_persist_across_processes", True),
    ("docker_shared_container_key", ""), ("docker_orphan_reaper", True), ("docker_snap_compat", False),
    ("docker_image_pinned", False),
)


def _ssh_config_from_config(config: dict[str, Any]) -> dict:
    """``ssh_config`` for :func:`_create_environment` (shared with the lazy ``ensure_task_env``)."""
    return {out: config.get(key, default) for out, key, default in _SSH_KEYS}


def _local_config_from_config(config: dict[str, Any]) -> dict:
    """``local_config`` for :func:`_create_environment` (TERMINAL_LOCAL_PERSISTENT, default on)."""
    return {"persistent": config.get("local_persistent", True)}


def _container_config_from_config(config: dict[str, Any]) -> dict:
    """``container_config`` for :func:`_create_environment` (shared with the lazy ``ensure_task_env``)."""
    return {k: config.get(k, d) for k, d in _CONTAINER_KEYS}


def _resources(cc: dict[str, Any]) -> dict:
    """Common sandbox resource kwargs (cpu/memory in MB/disk in MB/persistence)."""
    return {out: cc.get(key, default) for out, key, default in _RESOURCE_KEYS}


def _is_supported_vercel_runtime(runtime: str) -> bool:
    return not runtime or runtime in _SUPPORTED_VERCEL_RUNTIMES


# --- Environment builders. Signature: (*, image, cwd, timeout, cc, task_id, ssh_config, local_config, host_cwd)
# (env_type is only forwarded to the plugin-registry fallback, not to the built-in builders.)
def _build_local_env(*, cwd, timeout, local_config=None, **_):
    return _LocalEnvironment(cwd=cwd, timeout=timeout, persistent=(local_config or {}).get("persistent", True))


def _build_ssh_env(*, cwd, timeout, ssh_config, probe_only=False, **_):
    if not ssh_config or not ssh_config.get("host") or not ssh_config.get("user"):
        raise ValueError("SSH environment requires ssh_host and ssh_user to be configured")
    return _SSHEnvironment(host=ssh_config["host"], user=ssh_config["user"], port=ssh_config.get("port", 22),
                           key_path=ssh_config.get("key", ""), cwd=cwd, timeout=timeout, probe_only=probe_only,
                           persistent=ssh_config.get("persistent", True))


def _stamp_sandbox_home(env_obj, provider, cwd) -> None:
    """Record the provider's declared home on this environment: a declared absolute path whatever ``cwd`` the
    environment started in (``terminal.cwd`` may pick another directory), or, for ``~``, the path it resolved
    to inside THIS sandbox when it started there. Per environment, never per backend, so a second sandbox
    with a different home gets its own."""
    try:
        declared = provider.default_cwd
    except Exception:
        # A plugin's property must not take environment creation down; it just gets no home.
        logger.warning("%s.default_cwd raised; not recording a sandbox home", type(provider).__name__,
                       exc_info=True)
        declared = None
    if declared == "~":
        home = getattr(env_obj, "cwd", None) if cwd == "~" else None
    else:
        home = declared
    if isinstance(home, str) and home.startswith("/") and home != "/":
        try:
            env_obj._hermes_sandbox_home = home
        except AttributeError:
            pass


def _build_plugin_env(*, env_type, image, cwd, timeout, cc, task_id, **_):
    provider = _get_plugin_env_provider(env_type)
    if provider is not None:
        from hermes_cli.plugin_host import is_hosted_object
        if is_hosted_object(provider):
            # The environment would come back as an opaque placeholder, and its execute() would run outside
            # this process's interrupt, activity and terminal-scope state: refuse before the host starts a
            # sandbox nothing here could use or clean up.
            raise ValueError(
                f"terminal.backend is {env_type}, a plugin that runs in the plugin host (plugins.isolation: host), "
                f"and the host cannot hand its environment to Hermes, so commands cannot run in it. Set "
                f"plugins.isolation: in_process to use this backend.")
        env_obj = provider.create_environment(cwd=cwd, timeout=timeout, task_id=task_id, image=image,
                                              container_config=cc, **_)
        # Stamp the backend name so path-resolution and progress surfaces can identify plugin
        # backends without class-name sniffing. Test doubles may reject attributes.
        try:
            env_obj._hermes_backend_name = provider.name.strip().lower()
        except AttributeError:
            pass
        _stamp_sandbox_home(env_obj, provider, cwd)
        return env_obj
    try:
        from agent.terminal_env_registry import plugin_backend_names
        plugin_names = plugin_backend_names()
    except Exception:
        plugin_names = []
    known = ", ".join(f"'{n}'" for n in _BUILTIN_BACKENDS.split(", ") + list(plugin_names))
    from hermes_cli.left_core_migration import terminal_backend_install_hint
    raise ValueError(f"Unknown environment type: {env_type}. Use {known}{terminal_backend_install_hint(env_type)}")


# Built-in backend -> builder. Anything else is looked up in the plugin registry.
_ENV_BUILDERS = {"local": _build_local_env, "ssh": _build_ssh_env}


def _create_environment(env_type: str, image: str, cwd: str, timeout: int,
                        ssh_config: dict | None = None, container_config: dict | None = None,
                        local_config: dict | None = None, task_id: str = "default",
                        host_cwd: Optional[str] = None, probe_only: bool = False):
    """Create an execution environment (instance with ``execute()``) for *env_type*. ``image`` is ignored
    for local/ssh/vercel; ``container_config`` carries the container_*/docker_* resource keys; ``host_cwd`` is
    the host dir bound into Docker when cwd mounting is enabled. ``probe_only`` asks ssh for a throwaway
    connection with no remote setup/sync (the prompt-time probe). Unknown types fall through to plugin backends."""
    builder = _ENV_BUILDERS.get(env_type)
    kwargs = dict(image=image, cwd=cwd, timeout=timeout, cc=container_config or {}, task_id=task_id,
                  ssh_config=ssh_config, local_config=local_config, host_cwd=host_cwd, probe_only=probe_only)
    if builder is not None:
        env = builder(**kwargs)
    else:
        env = _build_plugin_env(env_type=env_type, **kwargs)
    # Backend tag for consumers that only hold the instance (cwd sanitizers on
    # live cached envs); __slots__ plugin providers simply keep going untagged.
    try:
        env.env_type = env_type
    except Exception:
        pass
    return env


# --- Requirement checkers: one generic path driven by _BACKEND_SPECS; optional fields, checked in order:
#   pre(config) -> True (satisfied) / False (rejected, already logged) / None (continue);
#   binary=(finder, version_arg, missing_log_or_None) runs ``<binary> <arg>``, ok iff rc == 0;
#   module=(find_spec name, log message when absent);  post(config) -> bool.
def _ssh_pre(config: dict[str, Any]) -> bool:
    if config.get("ssh_host") and config.get("ssh_user"):
        return True
    return _reject("the SSH host and user are not configured (TERMINAL_SSH_HOST / TERMINAL_SSH_USER); "
                   "run `hermes setup terminal` to enter them or pick the 'local' backend")


_BACKEND_SPECS = {"local": {}, "ssh": {"pre": _ssh_pre}}
_PROBE_FAILED_REASONS = {}


def _check_requirements(env_type: str, config: dict[str, Any]) -> bool:
    _record_unavailable_reason(None)
    spec = _BACKEND_SPECS[env_type]
    verdict = spec["pre"](config) if "pre" in spec else None
    if verdict is not None:
        return verdict
    if "binary" in spec:
        finder, arg, missing_msg = spec["binary"]
        executable = finder()
        if not executable:
            return _reject(missing_msg or f"the {env_type!r} backend's executable was not found")
        probe = subprocess.run([executable, arg], capture_output=True, timeout=5, stdin=subprocess.DEVNULL, check=False)
        if probe.returncode != 0:
            return _reject(f"{_PROBE_FAILED_REASONS[env_type]} (`{executable} {arg}` exited with code {probe.returncode})")
        return True
    if "module" in spec and importlib.util.find_spec(spec["module"][0]) is None:
        return _reject(spec["module"][1])
    if "post" in spec and not spec["post"](config):
        return _reject(f"the {env_type!r} backend is missing its API key or SDK")
    return True


def _check_plugin_requirements(config: dict[str, Any]) -> bool:
    _record_unavailable_reason(None)
    env_type = config["env_type"]
    provider = _get_plugin_env_provider(env_type)
    if provider is not None:
        ok = bool(provider.check_requirements(config))
        if not ok and _last_unavailable_reason is None:
            return _reject(f"the {env_type!r} plugin backend reported its requirements are not met")
        return ok
    from hermes_cli.left_core_migration import terminal_backend_install_hint
    return _reject(f"Unknown TERMINAL_ENV '{env_type}'. Use one of: {_BUILTIN_BACKENDS}, or a plugin-registered "
                   f"backend{terminal_backend_install_hint(env_type)}")


# Built-in backend -> requirements checker; unknown backends go to the plugin registry.
_REQUIREMENT_CHECKERS = {name: functools.partial(_check_requirements, name) for name in _BACKEND_SPECS}


def _check_vercel_sandbox_requirements(config):
    return _check_plugin_requirements({**config, "env_type": "vercel_sandbox"})
