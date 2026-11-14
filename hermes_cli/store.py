"""LINA's memory store: one Postgres with pgvector and the Honcho server, in Docker (lina#11).

``lina store install`` builds Honcho's image from ``services/honcho``, starts the database (schemas ``memory``,
``ctx``, ``ltm`` and ``vec`` with row-level security on ``agent_id``: ``services/store/initdb``), runs Honcho's
migrations as one job and starts the api and the deriver (``services/store/compose.yaml``). One store serves every
profile, so its files live under the LINA root, not a profile home: ``store/store.env`` (generated passwords,
ports and the data volume's name), ``store/honcho.env`` (the person's Honcho settings, such as LLM keys) and
``store/backups/`` (``hermes_cli/store_backup.py``).
"""

from __future__ import annotations

import json
import os
import secrets
import subprocess
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field
from importlib.resources import files
from pathlib import Path
from typing import Callable

from agent.embedding_client import DEFAULT_DIMENSIONS, home_model

# compose.yaml and initdb/ are package data of services.store (lina#145). Honcho's build context is compose.yaml's
# ../honcho, a source tree a wheel does not carry: preflight says so.
SERVICES_DIR = Path(files("services.store")).parent
COMPOSE_FILE = SERVICES_DIR / "store" / "compose.yaml"
HONCHO_DIR = SERVICES_DIR / "honcho"
PROJECT = "lina-store"
HONCHO_IMAGE = "lina-honcho:local"
WRITERS = ("honcho-api", "honcho-deriver")
SECRET_KEYS = ("LINA_STORE_PASSWORD", "LINA_APP_PASSWORD", "HONCHO_DB_PASSWORD")
DEFAULTS = {"LINA_STORE_PORT": "54320", "LINA_HONCHO_PORT": "8000", "LINA_STORE_VOLUME": "lina-store-pgdata"}
# store.env key -> role whose password it is (the cluster superuser is POSTGRES_USER=lina in compose.yaml).
ROLE_PASSWORDS = {"LINA_STORE_PASSWORD": "lina", "LINA_APP_PASSWORD": "lina_app", "HONCHO_DB_PASSWORD": "honcho"}
# Honcho's embedding settings: LINA's model at the width of vec.embeddings (migration 0003; the two widths are
# compared in tests/hermes_cli/test_store_honcho_env.py). Honcho's own default (1536) makes it refuse to start.
# ensure_env adds the keys honcho.env does not set, the model being the home's (home_model); what the person set
# there stays.
HONCHO_DEFAULTS = {"EMBEDDING_MODEL_CONFIG__TRANSPORT": "openai", "EMBEDDING_VECTOR_DIMENSIONS": str(DEFAULT_DIMENSIONS),
                   "EMBEDDING_MODEL_CONFIG__DIMENSIONS_MODE": "always"}
HONCHO_ENV_HEADER = "# Honcho settings (LLM_*, EMBEDDING_*, AUTH_*), one KEY=value per line. See services/honcho.\n"
# Honcho's model callers (services/honcho/src/config.py): caller -> (settings prefix, the switch that turns it off).
# Each takes its key from <prefix>__OVERRIDES__API_KEY or the variable <prefix>__OVERRIDES__API_KEY_ENV names, else
# LLM_<TRANSPORT>_API_KEY, and its address from <prefix>__OVERRIDES__BASE_URL, else (an LLM caller without its own key)
# LLM_<TRANSPORT>_BASE_URL. A partial <prefix>__* override keeps the default transport, openai, for every caller.
HONCHO_MODEL_CALLERS = {
    "embedding": ("EMBEDDING_MODEL_CONFIG", None), "deriver": ("DERIVER_MODEL_CONFIG", "DERIVER_ENABLED"),
    "summary": ("SUMMARY_MODEL_CONFIG", "SUMMARY_ENABLED"),
    "dream deduction": ("DREAM_DEDUCTION_MODEL_CONFIG", "DREAM_ENABLED"),
    "dream induction": ("DREAM_INDUCTION_MODEL_CONFIG", "DREAM_ENABLED"),
    **{f"dialectic {level}": (f"DIALECTIC_LEVELS__{level.upper()}__MODEL_CONFIG", None)
       for level in ("minimal", "low", "medium", "high", "max")},
}
_OFF = {"0", "false", "f", "no", "n", "off"}
# Run inside the api container: one GET <base>/models per argument, one JSON line each ("" = an HTTP answer came back,
# whatever its status; else why none did). No key is sent, so 401 counts as reached.
MODELS_PROBE = """import json, sys, urllib.error, urllib.request
for url in sys.argv[1:]:
    try:
        urllib.request.urlopen(url, timeout=5).close()
        answer = ""
    except urllib.error.HTTPError:
        answer = ""
    except Exception as exc:
        answer = str(getattr(exc, "reason", exc)) or type(exc).__name__
    print(json.dumps(answer))
"""


class StoreError(Exception):
    pass


def store_dir() -> Path:
    from hermes_constants import get_default_hermes_root
    return get_default_hermes_root() / "store"


def read_env(path: Path) -> dict[str, str]:
    values = {}
    if path.exists():
        for line in path.read_text(encoding="utf-8-sig").splitlines():
            key, sep, value = line.partition("=")
            if sep and not key.startswith("#"):
                values[key.strip()] = value.strip()
    return values


def _write_private(path: Path, text: str) -> None:
    tmp = path.with_name(path.name + ".tmp")
    fd = os.open(tmp, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(text)
    os.replace(tmp, path)


def write_env(path: Path, values: dict[str, str]) -> None:
    _write_private(path, "".join(f"{key}={value}\n" for key, value in values.items()))


def ensure_honcho_env(path: Path) -> None:
    """Create ``honcho.env`` and append the HONCHO_DEFAULTS it does not set, after the person's own lines."""
    text = path.read_text(encoding="utf-8-sig") if path.exists() else HONCHO_ENV_HEADER
    defaults = {**HONCHO_DEFAULTS, "EMBEDDING_MODEL_CONFIG__MODEL": home_model()}
    missing = {key: value for key, value in defaults.items() if key not in read_env(path)}
    if not missing and path.exists():
        return
    if text and not text.endswith("\n"):
        text += "\n"
    _write_private(path, text + "".join(f"{key}={value}\n" for key, value in missing.items()))


def ensure_env(directory: Path) -> dict[str, str]:
    """Create ``store.env`` on first use (random passwords, default ports) and keep what is already there."""
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    honcho_env = directory / "honcho.env"
    ensure_honcho_env(honcho_env)
    values = read_env(directory / "store.env")
    wanted = {**{key: secrets.token_urlsafe(24) for key in SECRET_KEYS}, **DEFAULTS,
              "LINA_HONCHO_ENV": str(honcho_env)}
    if any(key not in values for key in wanted):
        values = {**wanted, **values}
        write_env(directory / "store.env", values)
    return values


def honcho_model_settings(env: dict[str, str]) -> tuple[list[str], dict[str, str]]:
    """From ``honcho.env``: one line per missing key variable (with the callers that need it), and the base URL
    variables the callers use (name -> value). Key values are only tested for being set."""
    env = {key.upper(): value for key, value in env.items() if value}
    missing: dict[str, list[str]] = {}
    urls: dict[str, str] = {}
    for caller, (prefix, switch) in HONCHO_MODEL_CALLERS.items():
        if switch and env.get(switch, "").lower() in _OFF:
            continue
        transport = env.get(f"{prefix}__TRANSPORT", "openai").upper()
        key_env = env.get(f"{prefix}__OVERRIDES__API_KEY_ENV", "")
        own_key = f"{prefix}__OVERRIDES__API_KEY" in env or key_env.upper() in env
        if not own_key and f"LLM_{transport}_API_KEY" not in env:
            name = f"LLM_{transport}_API_KEY" + (f" (or {key_env}, named by {prefix}__OVERRIDES__API_KEY_ENV)"
                                                 if key_env else "")
            missing.setdefault(name, []).append(caller)
        url_var = f"{prefix}__OVERRIDES__BASE_URL"
        if url_var not in env and caller != "embedding" and not own_key:
            url_var = f"LLM_{transport}_BASE_URL"
        if url_var in env:
            urls[url_var] = env[url_var]
    return [f"{name} is not set in honcho.env ({', '.join(callers)} cannot call a model)"
            for name, callers in missing.items()], urls


def _address(url: str) -> str:
    """``host:port/path`` of a URL without any user:password part."""
    parts = urllib.parse.urlsplit(url)
    return f"{parts.hostname}{f':{parts.port}' if parts.port else ''}{parts.path}"


DOCKER_MISSING = ("Docker is not running or not installed. Install Docker (Docker Desktop on a Mac), start it, then "
                  "run `lina store install`.")


def _run(argv, *, env=None, capture=False, stdout=None, input=None) -> subprocess.CompletedProcess:
    try:
        return _run_process(argv, env=env, capture=capture, stdout=stdout, input=input)
    except FileNotFoundError as exc:
        raise StoreError(DOCKER_MISSING) from exc


def _run_process(argv, *, env, capture, stdout, input) -> subprocess.CompletedProcess:
    return subprocess.run(argv, env=env, text=stdout is None, check=False, timeout=3600, input=input,
                          stdout=stdout if stdout is not None else (subprocess.PIPE if capture else None),
                          stderr=subprocess.PIPE if capture else None,
                          stdin=None if input is not None else subprocess.DEVNULL)


def _http(method: str, url: str, body: bytes | None = None) -> tuple[int, bytes]:
    request = urllib.request.Request(url, data=body, method=method, headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=10) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()
    except OSError as exc:
        return 0, str(exc).encode()


@dataclass
class Store:
    directory: Path
    project: str = PROJECT
    overrides: dict[str, str] = field(default_factory=dict)  # beat store.env (used by the upgrade rehearsal)
    run: Callable = _run
    http: Callable = _http
    out: Callable = print

    @property
    def env_file(self) -> Path:
        return self.directory / "store.env"

    def values(self) -> dict[str, str]:
        return {**read_env(self.env_file), **self.overrides}

    def compose_argv(self, *args: str) -> list[str]:
        return ["docker", "compose", "-p", self.project, "-f", str(COMPOSE_FILE), "--env-file", str(self.env_file),
                *args]

    def _env(self) -> dict[str, str]:
        return {**os.environ, **self.overrides}

    def compose(self, *args: str, what: str, capture: bool = False, **kw) -> subprocess.CompletedProcess:
        result = self.run(self.compose_argv(*args), env=self._env(), capture=capture, **kw)
        if result.returncode != 0:
            detail = (result.stderr or "").strip().splitlines()[-1:] if capture and result.stderr else []
            raise StoreError(f"{what} failed" + (f": {detail[0]}" if detail else " (see the output above)"))
        return result

    def docker(self, *args: str, what: str, capture: bool = False, **kw) -> subprocess.CompletedProcess:
        result = self.run(["docker", *args], env=self._env(), capture=capture, **kw)
        if result.returncode != 0:
            raise StoreError(f"{what} failed" + (f": {(result.stderr or '').strip()}" if capture else ""))
        return result

    def psql(self, sql: str, *, what: str) -> str:
        return self.compose("exec", "-T", "db", "psql", "-U", "lina", "-d", "lina", "-X", "-q", "-At",
                            "-v", "ON_ERROR_STOP=1", what=what, capture=True, input=sql).stdout

    def running_services(self) -> set[str]:
        result = self.run(self.compose_argv("ps", "--status", "running", "--services"), env=self._env(),
                          capture=True)
        return set((result.stdout or "").split()) if result.returncode == 0 else set()

    def image_id(self, ref: str) -> str | None:
        result = self.run(["docker", "image", "inspect", "--format", "{{.Id}}", ref], capture=True)
        if result.returncode != 0:
            return None
        return (result.stdout or "").strip() or None

    def volume_exists(self, name: str) -> bool:
        return self.run(["docker", "volume", "inspect", name], capture=True).returncode == 0

    def start_db(self) -> None:
        self.compose("up", "-d", "--wait", "--quiet-pull", "db", what="starting the database")

    def migrate(self) -> None:
        """LINA's migrations (memory, ctx, vec), then Honcho's job (ltm)."""
        from hermes_cli.store_migrations import apply
        apply(lambda sql: self.psql(sql, what="the store migrations"), out=self.out)
        self.compose("--profile", "job", "run", "--rm", "honcho-migrate", what="the Honcho migration job")

    def start_writers(self) -> None:
        self.compose("up", "-d", "--wait", *WRITERS, what="starting the Honcho api and deriver")

    def stop_writers(self) -> list[str]:
        running = [name for name in WRITERS if name in self.running_services()]
        if running:
            self.compose("stop", *running, what="stopping the Honcho api and deriver")
        return running

    def set_role_passwords(self) -> None:
        """Make the roles' passwords match store.env (a restored cluster carries the passwords of its origin)."""
        values = self.values()
        self.psql("".join(f"ALTER ROLE {role} PASSWORD '{values[key]}';\n" for key, role in ROLE_PASSWORDS.items()),
                  what="setting the role passwords")

    def smoke(self) -> None:
        """The api answers, its database is reachable through it, and the migrations are recorded."""
        base = f"http://127.0.0.1:{self.values()['LINA_HONCHO_PORT']}"
        status, body = self.http("GET", base + "/health")
        if status != 200 or json.loads(body or b"{}").get("status") != "ok":
            raise StoreError(f"smoke test: GET /health answered {status} {body[:200]!r}")
        status, body = self.http("POST", base + "/v3/workspaces/list", b"{}")
        if status != 200:
            raise StoreError(f"smoke test: POST /v3/workspaces/list answered {status} {body[:200]!r}")
        revision = self.psql("SELECT version_num FROM ltm.alembic_version;", what="reading the Honcho revision")
        if not revision.strip():
            raise StoreError("smoke test: ltm.alembic_version is empty")
        self.out(f"Smoke test passed (Honcho revision {revision.strip()}).")

    def model_problems(self) -> list[str]:
        """What keeps Honcho from its models: missing key variables, and every base URL the api container cannot
        reach (one ``exec`` asking each ``<base>/models``), one line each naming the variable (lina#650)."""
        env = read_env(Path(self.values().get("LINA_HONCHO_ENV") or self.directory / "honcho.env"))
        problems, urls = honcho_model_settings(env)
        if not urls:
            return problems
        names = sorted(urls)
        probes = [urls[name].rstrip("/") + "/models" for name in names]
        try:
            answers = self.compose("exec", "-T", "honcho-api", "/app/.venv/bin/python", "-c", MODELS_PROBE, *probes,
                                   what="asking the model endpoints from the Honcho api container",
                                   capture=True).stdout.splitlines()
        except StoreError as exc:
            return [*problems, str(exc)]
        if len(answers) != len(probes):
            return [*problems, f"the endpoint probe in the Honcho api container answered {len(answers)} of "
                               f"{len(probes)} lines"]
        return problems + [f"Honcho cannot reach {name} ({_address(url)}: {json.loads(answer)})"
                           for name, url, answer in zip(names, probes, answers) if json.loads(answer)]

    def report_models(self) -> None:
        problems = self.model_problems()
        for line in problems:
            self.out(f"Model endpoints: {line}")
        if not problems:
            self.out("Model endpoints: Honcho has its keys and reaches every *_BASE_URL in honcho.env.")


def preflight(store: Store) -> None:
    if store.run(["docker", "info", "--format", "{{.ServerVersion}}"], capture=True).returncode != 0:
        raise StoreError(DOCKER_MISSING)
    if not (HONCHO_DIR / "Dockerfile").exists():
        raise StoreError(f"{HONCHO_DIR} is missing: the Honcho server source is needed to build its image.")


def build_honcho(store: Store) -> None:
    store.compose("build", "honcho-api", what="building the Honcho image from services/honcho")


def install(store: Store) -> None:
    """Fresh store: build, database, one migration job, api and deriver, smoke test. Existing store: upgrade."""
    preflight(store)
    values = ensure_env(store.directory)
    if store.volume_exists(values["LINA_STORE_VOLUME"]):
        from hermes_cli.store_upgrade import upgrade
        store.out(f"The store already exists (volume {values['LINA_STORE_VOLUME']}); upgrading it.")
        upgrade(store)
        return
    build_honcho(store)
    store.start_db()
    store.migrate()
    store.start_writers()
    store.smoke()
    store.out(f"The store is running: Postgres on 127.0.0.1:{values['LINA_STORE_PORT']}, Honcho on "
              f"127.0.0.1:{values['LINA_HONCHO_PORT']}. Settings: {store.directory}.")
    store.report_models()


def status(store: Store) -> int:
    if not store.env_file.exists():
        store.out("No store yet. Run `lina store install`.")
        return 1
    running = store.running_services()
    for name in ("db", *WRITERS):
        store.out(f"{name}: {'running' if name in running else 'not running'}")
    if "db" in running:
        schemas = store.psql("SELECT string_agg(nspname, ', ' ORDER BY nspname) FROM pg_namespace "
                             "WHERE nspname IN ('memory', 'ctx', 'ltm', 'vec');", what="listing the schemas")
        store.out(f"schemas: {schemas.strip()}")
    return 0 if running >= {"db", *WRITERS} else 1


def install_step(*, out=print) -> int:
    try:
        install(Store(store_dir(), out=out))
    except StoreError as exc:
        out(f"Store: {exc}")
        return 1
    return 0
