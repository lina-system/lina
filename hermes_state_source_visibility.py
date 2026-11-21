"""Read-time visibility of shared collector rows and items citing those rows."""

from __future__ import annotations


def platform_sql(alias: str) -> str:
    """Prefer the collector's native platform; older imports kept it in the channel or author."""
    return (f"coalesce(nullif({alias}.native->>'platform', ''), "
            f"CASE WHEN split_part({alias}.conversation_id, ':', 1) = 'messages' "
            f"THEN nullif(split_part({alias}.conversation_id, ':', 2), '') END, "
            f"split_part({alias}.author, ':', 1), '')")


def hidden_message(alias: str, *, prompt: bool = False) -> str:
    """SQL predicate for a message alias. Only fixed SQL literals can enter the query."""
    from hermes_cli.external_messages import disabled_sources, enabled

    kinds = ("mail", "slack") if prompt or not enabled() else disabled_sources()
    literals = [literal for kind, literal in (("mail", "'mail'"), ("slack", "'slack'")) if kind in kinds]
    if not literals:
        return "false"
    return f"({alias}.source = 'messages' AND {platform_sql(alias)} IN ({', '.join(literals)}))"


def visible_ref(column: str, *, prompt: bool = False) -> str:
    """Timeline entries retain either a base or a versioned msg: reference."""
    return (f"NOT EXISTS (SELECT 1 FROM memory.messages origin WHERE "
            f"origin.uid = substr({column}, 5) AND starts_with({column}, 'msg:') "
            f"AND {hidden_message('origin', prompt=prompt)})")


def visible_chunk() -> str:
    """A cached embedding of multiple messages inherits every contributor's restrictions."""
    return ("NOT EXISTS (SELECT 1 FROM memory.messages origin WHERE origin.message_id = ANY(c.message_ids) "
            f"AND {hidden_message('origin')})")


def visible_atom(column: str) -> str:
    """AtomsPhase joins its source references with spaces when it persists a claim."""
    return (f"NOT EXISTS (SELECT 1 FROM unnest(string_to_array({column}, ' ')) AS refs(ref) "
            f"WHERE NOT ({visible_ref('refs.ref')}))")
