"""The work settings (lina#256): which devices bid and how many runs each takes, which runners there are and the model
each starts with, the model aliases, defaults per mode and fallback, and the routing rules. They are these keys of the
LINA root's ``config.yaml``, the ones ``lina worker`` already reads on every command:

```yaml
devices:
  workers:
    build-a: {host: build-a, role: work, cap: 2, named_cap: 2, last: 0, fast: true}
    laptop:  {role: person}                      # the owner's own device: status only, never a cap, never a host
worker:
  default_runner: omo
  runners:
    codex: {type: command, argv: [codex, exec, "-"], on: true, model: "opencodex/impl-model:high",
            fallback_model: opencodex/small-model, access: workspace-write}
  models:
    aliases: {planner: {provider: opencodex, id: plan-model, thinking: medium}}
    defaults: {research: planner}
    fallback: opencodex/small-model
  routing:                                       # first match wins, else default_runner
    - {name: docs, when: {runner_label: runner/agy, labels_any: [docs], size: [S, M]}, runner: agy}
```

``role``, ``on``, ``model``, ``fallback_model``, ``access``, ``models.fallback`` and ``routing`` are new; today's
worker ignores them (the run-path half is lina#257). This module is the schema: the keys taken out of a config
(``snapshot``), validation, dotted paths and the field diff. Revisions live in ``lina_work_settings_store``.

A runner's ``env`` and any credential-like key are never part of the settings: they stay in ``config.yaml`` as they
are, never reach a revision, and a change that names one is refused.
"""

from __future__ import annotations

import copy
import json
import re
from pathlib import Path
from typing import Any

KEYS = (("devices", "workers"), ("worker", "default_runner"), ("worker", "runners"), ("worker", "models"),
        ("worker", "routing"))
NAME = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,40}$")
SECRET = re.compile(r"key|token|secret|password", re.IGNORECASE)
KEEP_OUT = "env"
ROLES = ("work", "files", "person")
ACCESS = ("read-only", "workspace-write", "full")
SIZES = ("S", "M", "L")
LIMITS = {"devices.workers": 128, "worker.runners": 64, "worker.routing": 64}
_BOOL, _INT, _STR, _STRS = "bool", "int", "str", "strs"
DEVICE = {"host": _STR, "role": _STR, "cap": _INT, "named_cap": _INT, "last": "last", "fast": _BOOL,
          "no_repo": _BOOL, "off": _BOOL, "auto": _BOOL, "lina": _STR}
RUNNER = {"type": _STR, "argv": _STRS, "follow_up_argv": _STRS, "model_args": _STRS, "on": _BOOL, "model": "model",
          "fallback_model": "model", "access": _STR, "bin": _STR, "node": _STR, "engine": _STR}
ALIAS = {"provider": _STR, "id": _STR, "thinking": _STR}
RULE = {"name": _STR, "when": "when", "runner": _STR}
WHEN = {"runner_label": _STR, "labels_any": _STRS, "size": _STRS}


class SettingsError(ValueError):
    """A change the settings refuse; ``errors`` = ``[{path, message}]``. Nothing was written."""

    def __init__(self, errors: list[dict]):
        self.errors = errors
        first = errors[0] if errors else {"path": "", "message": "refused"}
        more = f" (+{len(errors) - 1} more)" if len(errors) > 1 else ""
        super().__init__(f"{first['path']}: {first['message']}{more}")


def config_path(base: Path | None = None) -> Path:
    """The LINA root's ``config.yaml``, found the way ``lina worker`` finds its root."""
    from hermes_cli.worker import settings
    return settings.root(base).parent / "config.yaml"


def read_config(path: Path) -> dict:
    from hermes_cli.config_backend import config_exists, read_config_doc
    doc = read_config_doc(path) if config_exists(path) else None
    return doc if isinstance(doc, dict) else {}


def plain(value: Any) -> Any:
    """A deep copy as plain JSON types (the config parse may hand out ruamel containers)."""
    return json.loads(json.dumps(value, default=str))


def _clean(entry: Any) -> Any:
    if not isinstance(entry, dict):
        return entry
    return {k: v for k, v in entry.items() if k != KEEP_OUT and not SECRET.search(str(k))}


def snapshot(config: dict) -> dict:
    """The work-settings keys of a parsed config, without ``env`` or credential-like fields."""
    doc: dict = {}
    for top, key in KEYS:
        section = config.get(top)
        if isinstance(section, dict) and section.get(key) not in (None, {}, []):
            doc.setdefault(top, {})[key] = plain(section[key])
    workers = (doc.get("devices") or {}).get("workers")
    if isinstance(workers, dict):
        doc["devices"]["workers"] = {name: _clean(item) for name, item in workers.items()}
    runners = (doc.get("worker") or {}).get("runners")
    if isinstance(runners, dict):
        doc["worker"]["runners"] = {name: _clean(item) for name, item in runners.items()}
    return doc


# ---------------------------------------------------------------- dotted paths and diffs
def split(path: str) -> list[str]:
    parts = [p for p in str(path).split(".")]
    if not parts or any(not p for p in parts):
        raise SettingsError([{"path": str(path), "message": "not a dotted path"}])
    return parts


def get_path(doc: dict, path: str) -> Any:
    node: Any = doc
    for part in split(path):
        if not isinstance(node, dict) or part not in node:
            return None
        node = node[part]
    return node


def set_path(doc: dict, path: str, value: Any) -> dict:
    """Set one dotted key in *doc* (``None`` removes it and any parent left empty)."""
    parts = split(path)
    trail = [doc]
    for part in parts[:-1]:
        nxt = trail[-1].get(part)
        if not isinstance(nxt, dict):
            if value is None:
                return doc
            nxt = trail[-1][part] = {}
        trail.append(nxt)
    if value is None:
        trail[-1].pop(parts[-1], None)
        for depth in range(len(parts) - 1, 0, -1):
            if trail[depth]:
                break
            trail[depth - 1].pop(parts[depth - 1], None)
    else:
        trail[-1][parts[-1]] = copy.deepcopy(value)
    return doc


def merge_patch(doc: Any, patch: Any) -> Any:
    """RFC 7396 JSON Merge Patch."""
    if not isinstance(patch, dict):
        return copy.deepcopy(patch)
    out = dict(doc) if isinstance(doc, dict) else {}
    for key, value in patch.items():
        if value is None:
            out.pop(key, None)
        else:
            out[key] = merge_patch(out.get(key), value)
    return out


def updates(old: Any, new: Any, prefix: str = "") -> dict[str, Any]:
    """The fewest dotted sets (``None`` = remove) that turn *old* into *new*: untouched keys, and the comments on them,
    stay as they are in the file."""
    if not (isinstance(old, dict) and isinstance(new, dict)):
        return {} if old == new else {prefix: new}
    out: dict[str, Any] = {}
    for key in [*old, *(k for k in new if k not in old)]:
        path = f"{prefix}.{key}" if prefix else str(key)
        if key not in new:
            out[path] = None
        elif key not in old:
            out[path] = new[key]
        else:
            out.update(updates(old[key], new[key], path))
    return out


def _leaves(value: Any, prefix: str, out: dict) -> dict:
    if isinstance(value, dict) and value:
        for key, item in value.items():
            _leaves(item, f"{prefix}.{key}" if prefix else str(key), out)
    elif prefix:
        out[prefix] = value
    return out


def diff(old: dict, new: dict) -> list[dict]:
    """The field diff of two settings documents: ``[{path, from, to}]`` per leaf (a list is one leaf)."""
    before, after = _leaves(old, "", {}), _leaves(new, "", {})
    paths = [*before, *(p for p in after if p not in before)]
    return [{"path": p, "from": before.get(p), "to": after.get(p)} for p in paths if before.get(p) != after.get(p)]


# ---------------------------------------------------------------- validation
def _type_error(kind: str, value: Any) -> str | None:
    checks = {
        _BOOL: (lambda v: isinstance(v, bool), "true or false"),
        _INT: (lambda v: isinstance(v, int) and not isinstance(v, bool) and v >= 1, "a whole number of at least 1"),
        _STR: (lambda v: isinstance(v, str) and v.strip() != "", "a non-empty text"),
        _STRS: (lambda v: isinstance(v, list) and all(isinstance(i, str) and i for i in v), "a list of texts"),
        "last": (lambda v: isinstance(v, bool) or isinstance(v, int) and v >= 0, "true, false or a tier of 0 or more"),
        "model": (lambda v: isinstance(v, str) and v.strip() != "", "<provider>/<id>[:<effort>] or an alias"),
    }
    ok, want = checks[kind]
    return None if ok(value) else f"must be {want}"


def _fields(entry: Any, schema: dict, path: str, errors: list[dict]) -> dict:
    """Unknown, credential-like and ``env`` keys and wrong types of one entry; returns the entry (or {})."""
    if not isinstance(entry, dict):
        errors.append({"path": path, "message": "must be a mapping"})
        return {}
    for key, value in entry.items():
        where = f"{path}.{key}"
        if SECRET.search(str(key)):
            errors.append({"path": where, "message": "credential-like keys are never kept in the work settings"})
        elif key == KEEP_OUT:
            errors.append({"path": where, "message": "a runner's env is not written here: edit config.yaml"})
        elif key not in schema:
            errors.append({"path": where, "message": f"unknown key (known: {', '.join(schema)})"})
        elif schema[key] in (_BOOL, _INT, _STR, _STRS, "last", "model") and (why := _type_error(schema[key], value)):
            errors.append({"path": where, "message": why})
    return entry


def _named(table: Any, path: str, errors: list[dict]) -> dict:
    if table is None:
        return {}
    if not isinstance(table, dict):
        errors.append({"path": path, "message": "must be a mapping of names"})
        return {}
    limit = LIMITS.get(path)
    if limit and len(table) > limit:
        errors.append({"path": path, "message": f"at most {limit} entries"})
    for name in table:
        if not NAME.match(str(name)):
            errors.append({"path": f"{path}.{name}", "message": "a name is letters, digits, - and _ (up to 41)"})
    return table


def _devices(doc: dict, errors: list[dict]) -> None:
    workers = _named(get_path(doc, "devices.workers"), "devices.workers", errors)
    for name, item in workers.items():
        path = f"devices.workers.{name}"
        entry = _fields(item, DEVICE, path, errors)
        role = entry.get("role", "work")
        if role not in ROLES:
            errors.append({"path": f"{path}.role", "message": f"one of {', '.join(ROLES)}"})
        elif role == "person":
            errors += [{"path": f"{path}.{key}", "message": "a person device is never a node: no caps, no host"}
                       for key in ("cap", "named_cap", "last", "auto", "host") if key in entry]
        elif "host" not in entry:
            errors.append({"path": f"{path}.host", "message": "a work or files device needs its host (ssh alias or local)"})


def _model_error(value: Any, aliases: dict) -> str | None:
    if not isinstance(value, str) or not value.strip():
        return None  # the type check names it
    text = value.strip()
    if text.lower() in aliases or text.lower() == "default":
        return None
    provider, _, rest = text.partition("/")
    if not provider or not rest.split(":")[0]:
        return f"{text!r} is neither an alias ({', '.join(aliases) or 'none'}) nor <provider>/<id>[:<effort>]"
    return None


def _runners(doc: dict, aliases: dict, errors: list[dict]) -> dict:
    from hermes_cli.worker.runners import TYPES
    runners = _named(get_path(doc, "worker.runners"), "worker.runners", errors)
    for name, item in runners.items():
        path = f"worker.runners.{name}"
        entry = _fields(item, RUNNER, path, errors)
        if entry.get("type") not in TYPES:
            errors.append({"path": f"{path}.type", "message": f"one of {', '.join(TYPES)}"})
        if entry.get("type") == "command" and not entry.get("argv"):
            errors.append({"path": f"{path}.argv", "message": "a command runner needs its argv"})
        if "access" in entry and entry["access"] not in ACCESS:
            errors.append({"path": f"{path}.access", "message": f"one of {', '.join(ACCESS)}"})
        for key in ("model", "fallback_model"):
            if key in entry and (why := _model_error(entry[key], aliases)):
                errors.append({"path": f"{path}.{key}", "message": why})
    return runners


def _models(doc: dict, errors: list[dict]) -> dict:
    from hermes_cli.worker.models import THINKING
    from hermes_cli.worker.report import MODES
    models = _fields(get_path(doc, "worker.models") or {}, {"aliases": "", "defaults": "", "fallback": "model"},
                     "worker.models", errors)
    aliases = _named(models.get("aliases"), "worker.models.aliases", errors)
    for name, item in aliases.items():
        entry = _fields(item, ALIAS, f"worker.models.aliases.{name}", errors)
        if entry.get("thinking") is not None and entry["thinking"] not in THINKING:
            errors.append({"path": f"worker.models.aliases.{name}.thinking", "message": f"one of {', '.join(THINKING)}"})
        if not entry.get("provider") or not entry.get("id"):
            errors.append({"path": f"worker.models.aliases.{name}", "message": "an alias needs provider and id"})
    lowered = {str(k).lower(): v for k, v in aliases.items()}
    defaults = models.get("defaults") or {}
    if not isinstance(defaults, dict):
        errors.append({"path": "worker.models.defaults", "message": "must be a mapping of mode to model"})
        defaults = {}
    for mode, value in defaults.items():
        if mode not in MODES:
            errors.append({"path": f"worker.models.defaults.{mode}", "message": f"modes are {', '.join(MODES)}"})
        elif why := (_type_error("model", value) or _model_error(value, lowered)):
            errors.append({"path": f"worker.models.defaults.{mode}", "message": why})
    if "fallback" in models and (why := _model_error(models["fallback"], lowered)):
        errors.append({"path": "worker.models.fallback", "message": why})
    return lowered


def _routing(doc: dict, known: set[str], errors: list[dict]) -> None:
    rules = get_path(doc, "worker.routing")
    if rules is None:
        return
    if not isinstance(rules, list):
        errors.append({"path": "worker.routing", "message": "must be an ordered list of rules"})
        return
    if len(rules) > LIMITS["worker.routing"]:
        errors.append({"path": "worker.routing", "message": f"at most {LIMITS['worker.routing']} rules"})
    names: set[str] = set()
    for i, item in enumerate(rules):
        path = f"worker.routing[{i}]"
        rule = _fields(item, RULE, path, errors)
        if rule.get("name") in names:
            errors.append({"path": f"{path}.name", "message": f"{rule['name']!r} is used twice"})
        names.add(rule.get("name"))
        for key in ("name", "runner"):
            if key not in rule:
                errors.append({"path": f"{path}.{key}", "message": "required"})
        if "runner" in rule and rule["runner"] not in known:
            errors.append({"path": f"{path}.runner", "message": f"no runner {rule['runner']!r}"})
        when = _fields(rule.get("when") or {}, WHEN, f"{path}.when", errors)
        if any(size not in SIZES for size in when.get("size") or [] if isinstance(when.get("size"), list)):
            errors.append({"path": f"{path}.when.size", "message": f"sizes are {', '.join(SIZES)}"})
        if not when and i != len(rules) - 1:
            errors.append({"path": f"{path}.when", "message": "a rule that matches everything must be the last"})


def runner_table(doc: dict) -> dict[str, dict]:
    """The runners as ``lina worker`` sees them: omo is there even when not listed."""
    configured = get_path(doc, "worker.runners")
    table = {"omo": {"type": "omo"}}
    table.update({str(k): v for k, v in (configured or {}).items() if isinstance(v, dict)})
    return table


def default_runner(doc: dict) -> str:
    return str(get_path(doc, "worker.default_runner") or "omo")


def validate(doc: Any) -> list[dict]:
    """Every reason *doc* may not be written, as ``[{path, message}]``; ``[]`` when it may."""
    if not isinstance(doc, dict):
        return [{"path": "", "message": "the settings are a mapping"}]
    errors: list[dict] = []
    for top, value in doc.items():
        allowed = {key for t, key in KEYS if t == top}
        if not allowed:
            errors.append({"path": str(top), "message": "unknown key (known: devices, worker)"})
            continue
        for key in value if isinstance(value, dict) else []:
            if key not in allowed:
                errors.append({"path": f"{top}.{key}", "message": f"not a work setting (known: {', '.join(allowed)})"})
    _devices(doc, errors)
    aliases = _models(doc, errors)
    _runners(doc, aliases, errors)
    table = runner_table(doc)
    name = default_runner(doc)
    if name not in table:
        errors.append({"path": "worker.default_runner", "message": f"no runner {name!r} (known: {', '.join(table)})"})
    elif table[name].get("on") is False:
        errors.append({"path": "worker.default_runner", "message": f"runner {name!r} is off"})
    _routing(doc, set(table), errors)
    return errors


def check(doc: Any) -> None:
    errors = validate(doc)
    if errors:
        raise SettingsError(errors)
