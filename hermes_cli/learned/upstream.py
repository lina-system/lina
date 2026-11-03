"""Read upstream records without creating stores or running curation."""
from __future__ import annotations
from contextlib import contextmanager
import json
from hermes_constants import set_hermes_home_override, reset_hermes_home_override
from hermes_cli.learned import storage
from hermes_cli.work.env import current


@contextmanager
def profile_scope(home):
    token = set_hermes_home_override(home)
    try:
        yield
    finally:
        reset_hermes_home_override(token)


def skill_source(home):
    from tools.skill_ledger import ledger_path
    with profile_scope(home):
        return ledger_path()


def skill_blob(home, sha):
    from tools.skill_ledger import read_blob
    with profile_scope(home):
        return read_blob(sha)


def skill_facts(home):
    from agent.learning_graph import build_skill_nodes
    from tools.skill_usage import load_usage
    with profile_scope(home):
        usage = load_usage()
        nodes = build_skill_nodes([("profile", home / "skills")])
    return usage, nodes


def work_events(config):
    from hermes_cli.work import ledger, store, store_pg, store_sqlite
    env = current()
    if ledger.backend() == "sqlite":
        if not config.ledger.exists():
            return []
        db = store_sqlite.connect(config.ledger, env.owner, env.names, readonly=True)
    else:
        db = store_pg.connect(ledger._psql_argv(), env.owner, env.names)
    try:
        if db.dialect == "postgres":
            db.execute("BEGIN READ ONLY")
        lines = store.lines(db)
        return [(json.loads(line), storage.digest(line + "\n")) for _, line in lines]
    finally:
        db.close()
