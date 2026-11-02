"""Who is who in the record: the agents (LINA profiles) and the owner, by id or display name."""

from __future__ import annotations

from hermes_cli.work.common import die
from hermes_cli.work.env import current
from hermes_cli.work.locale import t


def owner() -> str:
    return current().owner


def agents() -> dict[str, str]:
    return current().agents


def name(who) -> str:
    """Display name of an agent or the owner; an unknown id as it is."""
    return current().names.get(who, who or "")


def agent_name(who, default: str | None = None) -> str:
    return agents().get(who, who if default is None else default)


def _lookup() -> dict[str, str]:
    table = {v: k for k, v in agents().items()}
    table.update({k: k for k in agents()})
    return table


def agent(v) -> str:
    if not v:
        die(t("--by <에이전트> 가 필요하다 ({names} 또는 프로필 이름)", names="/".join(agents().values())))
    p = _lookup().get(v.strip())
    if not p:
        die(t("모르는 에이전트: {v}", v=v))
    return p


def is_owner_word(v) -> bool:
    env = current()
    return (v or "").strip() in (env.owner, env.owner_name)


def owner_or_agent(v) -> str:
    """--by / --owner: an agent, or the owner (who owns to-dos only; the caller checks the kind)."""
    return owner() if is_owner_word(v) else agent(v)
