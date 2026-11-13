"""What ``lina work`` reads from the LINA root: the ``work:`` section of its ``config.yaml``, the profiles (the
agents), the Desk, and the work folder ``$LINA_HOME/work/`` (store file, checker state, notices, lock).

```yaml
work:
  locale: ko                     # labels and messages: ko or en (default en)
  owner: {id: owner, name: 주인}   # the person the record serves; to-dos may be theirs
  agents: {default: 알파}        # display names; default: every profile, its profile.yaml display_name
  areas: [Dev, Life]             # top-level areas `work add --area` accepts (unset: any)
  github: {owner: my-user, repos: {lina: lina-system}}   # owner of a bare repo name in gh:<repo>#n
  notice: {command: [my-notify, "{agent}"]}   # a line to an agent: body on stdin; unset: work/notices/<agent>.log
  session_queue: ~/sessions       # optional session spawner queue (running/ pending/ done/ *.json)
  probes: {todoist: [my-probe, todoist-done]}  # read-only probe: exit 0 done, 1 not done
  writing: {devices: [build-a]}   # extra device names the writing rules refuse (devices.workers is read too)
  store: {backend: postgres}      # postgres (the LINA store, schema work; default) or sqlite (work/work.db)
  freeze_wait_s: 1800
  run_never_started_s: 86400     # work check: a linked run never started, no worker job file, this old reads stopped
```

The values are read once per command (``bind``), never at import time.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from pathlib import Path

_CURRENT: list[Env] = []


@dataclass
class Env:
    base: Path
    config: dict
    locale: str
    owner: str
    owner_name: str
    agents: dict[str, str]
    devices: list[str] = field(default_factory=list)

    @property
    def root(self) -> Path:
        return self.base / "work"

    def section(self, name: str) -> dict:
        value = self.config.get(name)
        return value if isinstance(value, dict) else {}

    @property
    def names(self) -> dict[str, str]:
        return {**self.agents, self.owner: self.owner_name}

    @property
    def desk(self) -> Path | None:
        from hermes_cli.desk import get_desk_path
        return get_desk_path()


def _root_config(base: Path) -> dict:
    from hermes_cli.config_backend import config_exists, read_config_doc_readonly
    path = base / "config.yaml"
    doc = read_config_doc_readonly(path) if config_exists(path) else None
    return doc if isinstance(doc, dict) else {}


def _profile_agents() -> dict[str, str]:
    from hermes_cli.profiles import get_profile_dir, list_profile_names, read_profile_meta
    out = {}
    for name in list_profile_names():
        try:
            out[name] = read_profile_meta(get_profile_dir(name)).get("display_name") or name
        except (OSError, ValueError):
            out[name] = name
    return out


def _devices(config: dict, work: dict) -> list[str]:
    raw = (config.get("devices") or {}).get("workers") if isinstance(config.get("devices"), dict) else None
    names = []
    for name, item in (raw or {}).items() if isinstance(raw, dict) else ():
        names.append(str(name))
        if isinstance(item, dict) and isinstance(item.get("host"), str) and item["host"] != "local":
            names.append(item["host"])
    extra = (work.get("writing") or {}).get("devices") if isinstance(work.get("writing"), dict) else None
    names += [str(x) for x in extra or [] if x]
    return list(dict.fromkeys(n for n in names if n))


def load(base: Path | None = None) -> Env:
    from hermes_cli.lina_deps import lina_root
    from hermes_cli.work.locale import label
    base = base or lina_root()
    config = _root_config(base)
    work = config.get("work") if isinstance(config.get("work"), dict) else {}
    locale = str(work.get("locale") or "en")
    locale = locale if locale in ("ko", "en") else "en"
    owner = work.get("owner") if isinstance(work.get("owner"), dict) else {}
    owner_id = str(owner.get("id") or "owner")
    agents = work.get("agents") if isinstance(work.get("agents"), dict) else None
    agents = {str(k): str(v) for k, v in agents.items()} if agents else _profile_agents()
    agents.pop(owner_id, None)
    return Env(base, {**config, "work": work}, locale, owner_id,
               str(owner.get("name") or label("owner_word", locale)), agents, _devices(config, work))


def bind(env: Env) -> Env:
    _CURRENT[:] = [env]
    return env


def current() -> Env:
    if not _CURRENT:
        bind(load())
    return _CURRENT[0]


def reset() -> None:
    _CURRENT.clear()
