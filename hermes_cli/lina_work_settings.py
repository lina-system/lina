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

``role``, ``on``, ``model``, ``fallback_model``, ``access``, ``models.fallback`` and ``routing`` are new; the run
path reads all but ``role`` and ``access`` at submit (``lina_work_settings_route.decide``, lina#257). This module is the schema: the keys taken out of a config
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
FLAVORS = ("grok", "dsh", "agy", "claude", "codex")  # an ACP runner's agent; omitted = generic
LIMITS = {"devices.workers": 128, "worker.runners": 64, "worker.routing": 64}
_BOOL, _INT, _STR, _STRS = "bool", "int", "str", "strs"
DEVICE = {"host": _STR, "role": _STR, "cap": _INT, "named_cap": _INT, "last": "last", "fast": _BOOL,
          "no_repo": _BOOL, "off": _BOOL, "auto": _BOOL, "lina": _STR}
RUNNER = {"type": _STR, "argv": _STRS, "follow_up_argv": _STRS, "model_args": _STRS, "on": _BOOL, "model": "model",
          "fallback_model": "model", "access": _STR, "access_args": "access_args", "bin": _STR, "node": _STR, "engine": _STR,
          "flavor": _STR}
ALIAS = {"provider": _STR, "id": _STR, "thinking": _STR}
RULE = {"name": _STR, "when": "when", "runner": _STR}
WHEN = {"runner_label": _STR, "labels_any": _STRS, "size": _STRS}


class SettingsError(ValueError):
    """A change the settings refuse; ``errors`` = ``[{path, message}]``. Nothing was written."""

    def __init__(self, errors: list[dict]):
        self.errors = errors
        first = errors[0] if errors else {"path": "", "message": "refused"}
        more = f" (+{len(errors) - 1} more)" if len(errors) > 1 else ""
        where = f"{first['path']}: " if first["path"] else ""  # the root has no path to name
        super().__init__(f"{where}{first['message']}{more}")


def base() -> Path:
    """THE LINA root of the work settings: the one ``lina worker`` reads its config from
    (``hermes_cli.worker.settings.lina_home``, lina#227). The store, the CLI, the API router and ``lina devices``
    all resolve it here, so every writer and the worker see the same ``config.yaml``."""
    from hermes_cli.worker import settings
    return settings.lina_home()


def config_path(root: Path | None = None) -> Path:
    """``config.yaml`` of *root*, by default of :func:`base`."""
    return (root or base()) / "config.yaml"


UNREADABLE = ("cannot be read, does not parse or its top level is not a mapping; fix it first (nothing was written, "
              "the log names the cause)")


def read_config(path: Path) -> dict:
    """The parsed config through the writer seam's own guard (``require_readable_config_before_write``): a missing
    or empty file is ``{}``; an unreadable or unparseable file, or a root that is not a mapping (``false``, a list, a
    scalar), is refused, so it is never read as ``{}`` and then overwritten."""
    from hermes_cli.config import require_readable_config_before_write
    try:
        return require_readable_config_before_write(path)
    except RuntimeError as exc:  # its refusal; the guard has logged the cause
        raise SettingsError([{"path": "config.yaml", "message": UNREADABLE}]) from exc


def plain(value: Any) -> Any:
    """A deep copy as plain JSON types (the config parse may hand out ruamel containers)."""
    return json.loads(json.dumps(value, default=str))


def mapping(value: Any) -> dict:
    """*value* when it is a mapping, else ``{}``: for READING a document that may hold a wrong type (a hand edit).
    Never ``value or {}``: that also turns a present ``false``/``0``/``""``/``[]`` into "absent". Validation
    reports a present wrong type with its path instead (``_section``)."""
    return value if isinstance(value, dict) else {}


def _section(value: Any, schema: dict, path: str, errors: list[dict]) -> dict:
    """An optional mapping being validated: absent (``None``) is empty; anything else that is not a mapping, falsey
    or not, is one error with its path."""
    return {} if value is None else _fields(value, schema, path, errors)


def _excluded_key(key: Any) -> bool:
    return key == KEEP_OUT or bool(SECRET.search(str(key)))


# Mappings whose keys are names someone chose (a device, a runner, an alias, a mode), not fields: a runner named
# ``monkey`` is a setting. Every other key at any depth is a field, and a credential-like field is never captured.
NAME_TABLES = ("devices.workers", "worker.runners", "worker.models.aliases", "worker.models.defaults")


def _excluded_at(path: str, key: Any) -> bool:
    return path not in NAME_TABLES and _excluded_key(key)


def _child(path: str, key: Any) -> str:
    return f"{path}.{key}" if path else str(key)


def scrub(value: Any, path: str = "") -> Any:
    """*value* without ``env`` or any credential-like field at any depth. An entry of a name table that held only
    such fields is not a setting at all, and neither is a table left empty by that."""
    if isinstance(value, list):
        return [scrub(item, f"{path}[{i}]") for i, item in enumerate(value)]
    if not isinstance(value, dict):
        return value
    out = {}
    for key, item in value.items():
        if _excluded_at(path, key):
            continue
        cleaned = scrub(item, _child(path, key))
        if path in NAME_TABLES and item and cleaned == {}:
            continue
        out[key] = cleaned
    return out


def snapshot(config: dict) -> dict:
    """The work-settings keys of a parsed config, without ``env`` or credential-like fields at any depth: what is
    stored in a revision, diffed into the audit and served."""
    doc: dict = {}
    for top, key in KEYS:
        section = config.get(top)
        if isinstance(section, dict) and section.get(key) not in (None, {}, []):
            doc.setdefault(top, {})[key] = scrub(plain(section[key]), f"{top}.{key}")
    for top, key in KEYS:  # a table left empty by that is no setting either
        if mapping(doc.get(top)).get(key) == {}:
            del doc[top][key]
            if not doc[top]:
                del doc[top]
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


TURN_OFF = {"devices.workers": "off: true", "worker.runners": "on: false"}


def _own_fields(path: str, raw: Any) -> list[str]:
    """The excluded fields the file holds directly in the mapping at *path*."""
    return sorted(str(k) for k in raw if _excluded_at(path, k)) if isinstance(raw, dict) else []


def _held(path: str, raw: Any) -> bool:
    """Whether the file holds excluded fields (a runner's ``env``, credential-like keys) at or under *path*."""
    if isinstance(raw, list):
        return any(_held(f"{path}[{i}]", item) for i, item in enumerate(raw))
    if not isinstance(raw, dict):
        return False
    return bool(_own_fields(path, raw)) or any(_held(_child(path, k), v) for k, v in raw.items())


def _held_fields(path: str, raw: Any) -> list[str]:
    if isinstance(raw, list):
        return sorted({f for i, item in enumerate(raw) for f in _held_fields(f"{path}[{i}]", item)})
    if not isinstance(raw, dict):
        return []
    return sorted({*_own_fields(path, raw), *(f for k, v in raw.items() for f in _held_fields(_child(path, k), v))})


def _refusal(path: str, fields: list[str]) -> dict:
    table = path.rsplit(".", 1)[0]
    off = f"turn it off with {TURN_OFF[table]}, or " if table in TURN_OFF and path.count(".") == table.count(".") + 1 else ""
    return {"path": path, "message": f"has fields the work settings don't manage ({', '.join(fields)}); {off}"
                                      "remove those fields in config.yaml first"}


def _remnants_at(old: Any, new: Any, raw: Any, path: str, errors: list[dict]) -> None:
    if isinstance(raw, list):  # a list is written whole: it can keep nothing the settings do not hold
        if old != new and _held(path, raw):
            errors.append(_refusal(path, _held_fields(path, raw)))
        return
    if not isinstance(raw, dict):
        return
    own = _own_fields(path, raw)
    if own and old is not None and not isinstance(new, dict):  # removed, or replaced by a non-mapping
        errors.append(_refusal(path, own))
        return
    for key, item in raw.items():
        if not _excluded_at(path, key):
            _remnants_at(old.get(key) if isinstance(old, dict) else None, new.get(key) if isinstance(new, dict) else None,
                         item, _child(path, key), errors)


def remnants(old: dict, new: dict, raw: dict) -> list[dict]:
    """Changes that would drop or strand fields the settings do not own (a runner's ``env``, credential-like keys at
    any depth): removing a mapping that holds them would leave it behind without its own fields (a runner without
    ``type``, which ``lina worker`` builds as a broken runner), and a list (``worker.routing``) is written whole, so
    changing one that holds them would delete them. Such a change, or a rollback that would make it, is refused; a
    runner or device is stopped by turning it off."""
    errors: list[dict] = []
    for top, key in KEYS:
        path = f"{top}.{key}"
        _remnants_at(get_path(old, path), get_path(new, path), get_path(raw, path), path, errors)
    return errors


def apply_updates(doc: dict, sets: dict[str, Any]) -> dict:
    """A copy of *doc* with every dotted set (``None`` = remove) applied, with the navigation ``hermes config set``
    uses: escaped dots (``a\\.b``) and an existing literal dotted key (``glm-5.3``) over blind splitting."""
    from hermes_cli.config import _set_nested, _unset_nested
    out = copy.deepcopy(doc)
    for path, value in sets.items():
        if value is None:
            _unset_nested(out, path)
        else:
            _set_nested(out, path, copy.deepcopy(value))
    return out


def _kept_updates(old: Any, new: Any, path: str, raw: Any) -> dict[str, Any]:
    """``updates``, except that a mapping holding excluded fields in the file is never set or removed whole: its
    included fields are, one by one, so the excluded ones stay where they are."""
    if old == new:
        return {}
    if not _held(path, raw) or not (new is None or isinstance(new, dict)) or not (old is None or isinstance(old, dict)):
        return updates(old, new, path)
    old, new = mapping(old), mapping(new)  # both are None or a mapping here
    out: dict[str, Any] = {}
    for key in [*old, *(k for k in new if k not in old)]:
        out.update(_kept_updates(old.get(key), new.get(key), f"{path}.{key}", raw.get(key)))
    return out


def settings_updates(old: dict, new: dict, raw: dict | None = None) -> dict[str, Any]:
    """``updates`` for two settings documents, emitted only at or below a work-settings key (``KEYS``): never at
    ``devices``, ``worker`` or another section the settings share with other keys, so a write never replaces or
    removes ``worker.hub``, the limits or the write gate's own keys. *raw* is the parsed file: a runner or device
    that holds excluded fields there is written field by field, so they stay (removing such an entry is refused
    before this, see ``remnants``)."""
    out: dict[str, Any] = {}
    for top, key in KEYS:
        path = f"{top}.{key}"
        out.update(_kept_updates(get_path(old, path), get_path(new, path), path, get_path(mapping(raw), path)))
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
        if not isinstance(role, str) or role not in ROLES:  # the lookups below run only on a text
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
        if not isinstance(entry.get("type"), str) or entry["type"] not in TYPES:
            errors.append({"path": f"{path}.type", "message": f"one of {', '.join(TYPES)}"})
        if entry.get("type") == "command" and not entry.get("argv"):
            errors.append({"path": f"{path}.argv", "message": "a command runner needs its argv"})
        if entry.get("type") == "acp" and not entry.get("argv"):
            errors.append({"path": f"{path}.argv", "message": "an acp runner needs its argv: a non-empty list of non-empty texts"})
        if "flavor" in entry and isinstance(entry["flavor"], str):
            if entry.get("type") != "acp":
                errors.append({"path": f"{path}.flavor", "message": "only an acp runner has a flavor"})
            elif entry["flavor"] not in FLAVORS:
                errors.append({"path": f"{path}.flavor", "message": f"one of {', '.join(FLAVORS)}"})
        if "access" in entry and (not isinstance(entry["access"], str) or entry["access"] not in ACCESS):
            errors.append({"path": f"{path}.access", "message": f"one of {', '.join(ACCESS)}"})
        if "access_args" in entry:
            args = entry["access_args"]
            if not isinstance(args, dict):
                errors.append({"path": f"{path}.access_args", "message": "must be a mapping of access modes to arguments"})
            else:
                for mode, argv in args.items():
                    if mode not in ACCESS or not isinstance(argv, list) or not all(isinstance(arg, str) for arg in argv):
                        errors.append({"path": f"{path}.access_args.{mode}",
                                       "message": f"a mode from {', '.join(ACCESS)} with a list of strings"})
        for key in ("model", "fallback_model"):
            if key in entry and (why := _model_error(entry[key], aliases)):
                errors.append({"path": f"{path}.{key}", "message": why})
    return runners


def _models(doc: dict, errors: list[dict]) -> dict:
    from hermes_cli.worker.models import THINKING
    from hermes_cli.worker.report import MODES
    models = _section(get_path(doc, "worker.models"), {"aliases": "", "defaults": "", "fallback": "model"},
                      "worker.models", errors)
    aliases = _named(models.get("aliases"), "worker.models.aliases", errors)
    for name, item in aliases.items():
        entry = _fields(item, ALIAS, f"worker.models.aliases.{name}", errors)
        thinking = entry.get("thinking")
        if thinking is not None and (not isinstance(thinking, str) or thinking not in THINKING):
            errors.append({"path": f"worker.models.aliases.{name}.thinking", "message": f"one of {', '.join(THINKING)}"})
        if not entry.get("provider") or not entry.get("id"):
            errors.append({"path": f"worker.models.aliases.{name}", "message": "an alias needs provider and id"})
    lowered = {str(k).lower(): v for k, v in aliases.items()}
    defaults = {} if models.get("defaults") is None else models["defaults"]
    if not isinstance(defaults, dict):
        errors.append({"path": "worker.models.defaults", "message": "must be a mapping of mode to model"})
        defaults = {}
    for mode, value in defaults.items():
        if not isinstance(mode, str) or mode not in MODES:
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
        _rule(i, item, len(rules), names, known, errors)


def _rule(i: int, item: Any, count: int, names: set[str], known: set[str], errors: list[dict]) -> None:
    """One routing rule. Each lookup (name used twice, runner known, size allowed) runs only on a value whose type
    passed: a wrong type is one error with its path, never an exception."""
    path = f"worker.routing[{i}]"
    rule = _fields(item, RULE, path, errors)
    name, runner = rule.get("name"), rule.get("runner")
    if isinstance(name, str):
        if name in names:
            errors.append({"path": f"{path}.name", "message": f"{name!r} is used twice"})
        names.add(name)
    for key in ("name", "runner"):
        if key not in rule:
            errors.append({"path": f"{path}.{key}", "message": "required"})
    if isinstance(runner, str) and runner not in known:
        errors.append({"path": f"{path}.runner", "message": f"no runner {runner!r}"})
    when = _section(rule.get("when"), WHEN, f"{path}.when", errors)
    sizes = when.get("size")
    if isinstance(sizes, list) and any(not isinstance(s, str) or s not in SIZES for s in sizes):
        errors.append({"path": f"{path}.when.size", "message": f"sizes are {', '.join(SIZES)}"})
    if rule.get("when") in (None, {}) and i != count - 1:
            errors.append({"path": f"{path}.when", "message": "a rule that matches everything must be the last"})


def runner_table(doc: dict) -> dict[str, dict]:
    """The runners as ``lina worker`` sees them: omo is there even when not listed."""
    configured = get_path(doc, "worker.runners")
    table = {"omo": {"type": "omo"}}
    if isinstance(configured, dict):
        table.update({str(k): v for k, v in configured.items() if isinstance(v, dict)})
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
        if not isinstance(value, dict):
            errors.append({"path": str(top), "message": "must be a mapping"})
            continue
        for key in value:
            if key not in allowed:
                errors.append({"path": f"{top}.{key}", "message": f"not a work setting (known: {', '.join(allowed)})"})
    _devices(doc, errors)
    aliases = _models(doc, errors)
    _runners(doc, aliases, errors)
    table = runner_table(doc)
    name = default_runner(doc)
    chosen = get_path(doc, "worker.default_runner")
    if chosen is not None and (why := _type_error(_STR, chosen)):
        errors.append({"path": "worker.default_runner", "message": why})
    elif name not in table:
        errors.append({"path": "worker.default_runner", "message": f"no runner {name!r} (known: {', '.join(table)})"})
    elif table[name].get("on") is False:
        errors.append({"path": "worker.default_runner", "message": f"runner {name!r} is off"})
    _routing(doc, set(table), errors)
    return errors


def check(doc: Any) -> None:
    errors = validate(doc)
    if errors:
        raise SettingsError(errors)
