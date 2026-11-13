"""``lina worker settings`` (lina#256): show, change, check and undo the work settings (``hermes_cli/lina_work_settings``).

Every change goes through the same layer as the client API: one revision and one audit line per change, written as
the OS user from device ``local``. A refusal prints ``lina worker settings: <one line>`` and exits 2, like
``lina worker``; ``validate`` exits 1 when the settings are not valid.
"""

from __future__ import annotations

import json
import os
import shlex
import subprocess
import sys
import tempfile
import time
from pathlib import Path

import hermes_yaml


def _store():
    from hermes_cli.lina_work_settings_store import Store
    return Store()


def _value(value) -> str:
    if value is None:
        return "-"
    return value if isinstance(value, str) else json.dumps(value, ensure_ascii=False)


def _changes(rev, changes: list[dict]) -> int:
    if not changes:
        print(f"{rev.id}: no change")
    for change in changes:
        print(f"{rev.id}: {change['path']}: {_value(change['from'])} → {_value(change['to'])}")
    return 0


def _show(args) -> int:
    from hermes_cli.lina_work_settings_store import rev_number
    store = _store()
    rev = store.revision(rev_number(args.rev) or 0) if args.rev else store.current()
    if args.json:
        print(json.dumps({"revision": {"id": rev.id, **{k: (rev.audit or {}).get(k) for k in ("at", "by", "base")}},
                          "settings": rev.doc}, ensure_ascii=False))
    else:
        print(f"# {rev.id}")
        print(hermes_yaml.safe_dump(rev.doc, sort_keys=False).rstrip() if rev.doc else "{}")
    return 0


def _set(args) -> int:
    from hermes_cli.lina_work_settings import set_path
    from hermes_cli.lina_work_settings_store import cli_by
    value = hermes_yaml.safe_load(args.value) if args.value.strip() else None
    return _changes(*_store().apply(lambda doc: set_path(doc, args.path, value), cli_by()))


def _edit(args) -> int:
    from hermes_cli.lina_work_settings import SettingsError, check
    from hermes_cli.lina_work_settings_store import cli_by
    store = _store()
    rev = store.current()
    fd, name = tempfile.mkstemp(prefix="work-settings-", suffix=".yaml")
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        fh.write(f"# {rev.id}: save and quit to make a new revision\n")
        fh.write(hermes_yaml.safe_dump(rev.doc, sort_keys=False) if rev.doc else "{}\n")
    editor = shlex.split(os.environ.get("VISUAL") or os.environ.get("EDITOR") or "vi")
    subprocess.run([*editor, name], check=False, stdin=None)  # health: allow HX006 -- an interactive editor on this terminal runs as long as the person edits
    new = _load(Path(name))
    if new is None:  # saved empty (blank or comments only): like an empty commit message, nothing is changed
        Path(name).unlink(missing_ok=True)
        print(f"{rev.id}: the file was saved empty; nothing changed")
        return 0
    try:
        check(new)  # a root that is not a mapping (false, 0, "", []) is refused here, never read as {}
    except SettingsError as exc:
        first = exc.errors[0]
        raise SettingsError([{**first, "message": f"{first['message']} (your text is kept in {name})"},
                             *exc.errors[1:]]) from exc
    Path(name).unlink(missing_ok=True)
    return _changes(*store.apply(lambda _doc: new, cli_by(), expect=rev.etag))


def _load(path: Path):
    """A YAML file as parsed: ``None`` only when it holds nothing (blank or comments only). Every other value, falsey
    or not, is returned as it is, so validation can refuse a root that is not a mapping."""
    from ruamel.yaml import YAMLError

    from hermes_cli.lina_work_settings import SettingsError
    try:
        return hermes_yaml.safe_load(path.read_text(encoding="utf-8-sig"))
    except YAMLError as exc:
        raise SettingsError([{"path": "", "message": f"{path} does not parse as YAML"}]) from exc


def _validate(args) -> int:
    from hermes_cli.lina_work_settings import validate
    if args.file:
        loaded = _load(Path(args.file))
        doc = {} if loaded is None else loaded  # only an empty file is empty settings
    else:
        doc = _store().current().doc
    errors = validate(doc)
    for error in errors:
        print(f"{error['path']}: {error['message']}")
    if not errors:
        print("ok")
    return 1 if errors else 0


def _when(at) -> str:
    return time.strftime("%m-%d %H:%M", time.localtime(at)) if isinstance(at, (int, float)) else "-"


def _history(args) -> int:
    store = _store()
    store.current()  # an outside edit becomes its revision before the history is shown
    for line in store.history(args.limit):
        by = line.get("by") or {}
        paths = ", ".join(c["path"] for c in line.get("changes") or [])
        print(f"{line['rev']:<6} {_when(line.get('at'))}  {by.get('kind')} {by.get('principal') or '-'}@"
              f"{by.get('device') or '-'}  {paths or '(no field change)'}")
    return 0


def _diff(args) -> int:
    from hermes_cli.lina_work_settings import diff
    from hermes_cli.lina_work_settings_store import rev_number
    store = _store()
    newest = store.current().n
    b = rev_number(args.b) if args.b else newest
    a = rev_number(args.a) if args.a else max(0, b - 1)
    for change in diff(store.revision(a).doc, store.revision(b).doc):
        print(f"{change['path']}: {_value(change['from'])} → {_value(change['to'])}")
    return 0


def _rollback(args) -> int:
    from hermes_cli.lina_work_settings_store import cli_by
    rev, changes = _store().rollback(cli_by())
    back = rev.audit or {}
    print(f"{rev.id} = r{int(back.get('base', 'r1')[1:]) - 1} (rollback of {back.get('rollback_of')})")
    for change in changes:
        print(f"  {change['path']}: {_value(change['from'])} → {_value(change['to'])}")
    return 0


def _route(args) -> int:
    from hermes_cli.lina_work_settings_route import route
    labels = [x.strip() for x in (args.labels or "").split(",") if x.strip()]
    found = route(_store().current().doc, labels, args.size)
    print(f"{found['runner']} ({'rule ' + found['rule'] if found['rule'] else 'default'})")
    return 0


def _models(args) -> int:
    from hermes_cli.lina_models import _table
    from hermes_cli.lina_work_settings_models import view
    data = view()
    if args.json:
        print(json.dumps(data, ensure_ascii=False))
        return 0
    rows = [(m["provider"], m["id"], m["state"], "-" if m["left"] is None else f"{m['left']}%", _when(m["resetAt"]))
            for m in data["models"]]
    for line in _table(("PROVIDER", "MODEL", "STATE", "LEFT", "RESET"), rows):
        print(line)
    for line in data["warnings"]:
        print(line)
    for error in data["errors"]:
        print(f"{error['source']}: {error['message']}", file=sys.stderr)
    return 0


_ACTIONS = {"show": _show, "set": _set, "edit": _edit, "validate": _validate, "history": _history, "diff": _diff,
            "rollback": _rollback, "route": _route, "models": _models}


def _cmd(args) -> None:
    from hermes_cli.lina_deps import DepsError
    from hermes_cli.lina_work_settings import SettingsError
    from hermes_cli.lina_work_settings_store import Stale
    try:
        sys.exit(_ACTIONS[args.settings_action or "show"](args))
    except (SettingsError, Stale, KeyError, OSError, DepsError) as exc:
        print(f"lina worker settings: {exc}", file=sys.stderr)
        sys.exit(2)


def add_parser(sub) -> None:
    """Attach ``settings`` under ``lina worker``."""
    parser = sub.add_parser("settings", help="Show, change and undo the work settings (devices, runners, models, "
                                             "routing), each change a revision")
    ss = parser.add_subparsers(dest="settings_action")
    show = ss.add_parser("show", help="The settings now (or --rev rN)")
    show.add_argument("--json", action="store_true")
    show.add_argument("--rev")
    put = ss.add_parser("set", help="One dotted key, e.g. worker.runners.codex.model codex/x:high (null removes)")
    put.add_argument("path")
    put.add_argument("value", help="a YAML value: 3, false, codex/x:high, {host: local}, null")
    ss.add_parser("edit", help="Edit the settings in $EDITOR, saved as one revision")
    check = ss.add_parser("validate", help="Check the settings now, or a YAML file (exit 1 when not valid)")
    check.add_argument("file", nargs="?")
    history = ss.add_parser("history", help="Revisions, newest first")
    history.add_argument("--limit", type=int, default=20)
    diff = ss.add_parser("diff", help="Field diff between two revisions (default: the newest and the one before)")
    diff.add_argument("a", nargs="?")
    diff.add_argument("b", nargs="?")
    ss.add_parser("rollback", help="Write the revision before the newest as a new revision")
    route = ss.add_parser("route", help="Which runner the routing rules pick for an issue")
    route.add_argument("--labels", help="the issue's labels, comma-separated")
    route.add_argument("--size", choices=["S", "M", "L"])
    models = ss.add_parser("models", help="magpie's models with state, quota left and reset")
    models.add_argument("--json", action="store_true")
    parser.set_defaults(func=_cmd, json=False, rev=None)  # no subcommand = show, which reads both
