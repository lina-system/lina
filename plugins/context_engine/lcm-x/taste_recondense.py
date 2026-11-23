"""Recondense only LCM-X summaries that name a forgotten taste."""

from __future__ import annotations

import re
from dataclasses import replace
from pathlib import Path
from typing import TYPE_CHECKING

from .dag import SummaryNode
from .escalation import SweepBudgetExhausted, summarize_with_escalation
from .reconcile import _emission_identity
from .sqlite_session import is_store_connection
from .sqlite_util import _run_sqlite_write_with_snapshot_retry
from .taste_forget import names_target, pending_targets, target_spans, taste_instructions
from .tokens import count_tokens
from .vector_store import VectorStore

if TYPE_CHECKING:
    from .engine import LCMEngine


def _omit_naming_clauses(text: str, targets: dict[int, set[str]]) -> str:
    """Keep clauses that do not name the retired label verbatim."""
    from hermes_cli.heart.taste_context import normalized

    clauses = re.split(r"(?<=[.!?;。！？；])(?=\s|$)|(?=\n)", text)
    spans = iter(target_spans(normalized(text), targets))
    match = next(spans, None)
    consumed = covered_until = 0
    kept = []
    for clause in clauses:
        end = consumed + len(normalized(clause))
        while match is not None and match[0] < end:
            covered_until = max(covered_until, match[1])
            match = next(spans, None)
        if covered_until <= consumed:
            kept.append(clause)
        consumed = end
    return "".join(kept).strip()


def _fallback(node: SummaryNode, targets: dict[int, set[str]]) -> SummaryNode:
    summary = _omit_naming_clauses(node.summary, targets) or "Taste detail omitted. See the source history."
    return replace(
        node, summary=summary, token_count=count_tokens(summary),
        expand_hint=_omit_naming_clauses(node.expand_hint, targets),
    )


def _recondense_node(
    engine: LCMEngine, node: SummaryNode, targets: dict[int, set[str]],
) -> tuple[SummaryNode, int, str]:
    config = engine._config
    if node.source_type == "messages":
        raw = engine._store.get_batch(node.source_ids)
        messages = [raw[row_id] for row_id in node.source_ids if row_id in raw]
        source = engine._serialize_messages(messages, node.session_id)
    else:
        source = "\n\n---\n\n".join(child.summary for child in engine._dag.get_source_nodes(node))
    source = (
        f"Previous summary:\n{node.summary}\n\n"
        f"Source material:\n{source}"
    )
    instructions = taste_instructions(engine._hermes_home, config.custom_instructions, forgetting=True)
    instructions += (
        "\nRe-summarize the previous summary from the source material. "
        "Retain every unrelated fact, decision, constraint and identifier. "
        "Describe a past taste-related event without repeating its target. "
        "Omit the target from the summary and the expand hint."
    )
    provenance: dict[str, str] = {}
    source_tokens = count_tokens(source)
    try:
        summary, level = summarize_with_escalation(
            text=source, source_tokens=source_tokens, token_budget=max(64, node.token_count), depth=node.depth,
            model=config.summary_model, fallback_models=config.summary_fallback_models,
            reasoning_effort=config.summary_reasoning_effort, timeout=config.summary_timeout_ms / 1000,
            circuit_breaker=engine._summary_circuit_breaker, spend_guard=engine._summary_spend_guard,
            l2_budget_ratio=config.l2_budget_ratio, l3_truncate_tokens=config.l3_truncate_tokens,
            prompt_version=config.summary_prompt_version, custom_instructions=instructions, provenance=provenance,
            route_key_prefix="taste-forget:", budget=engine._foreground_call_budget(),
        )
    except SweepBudgetExhausted:
        return _fallback(node, targets), 3, "deterministic"
    if level == 3 or not summary.strip() or names_target(summary, targets):
        return _fallback(node, targets), 3, "deterministic"
    hint = _omit_naming_clauses(node.expand_hint, targets) or engine._extract_expand_hint(summary)
    return replace(
        node, summary=summary, token_count=count_tokens(summary), expand_hint=hint,
    ), level, provenance.get("model", "")


def _publish(
    engine: LCMEngine, old: SummaryNode, new: SummaryNode, level: int, model: str,
) -> bool:
    dag = engine._dag
    conn = dag.connection
    if conn is None:
        return False

    def update() -> bool:
        changed = conn.execute(
            "UPDATE summary_nodes SET summary=?, token_count=?, expand_hint=? "
            "WHERE node_id=? AND session_id=? AND summary=? AND coalesce(expand_hint, '')=?",
            (new.summary, new.token_count, new.expand_hint, old.node_id, old.session_id, old.summary, old.expand_hint),
        ).rowcount
        if not changed:
            return False
        conn.execute(
            "INSERT INTO summary_node_provenance(node_id, escalation_level, model, created_at) "
            "VALUES (?, ?, ?, ?) ON CONFLICT(node_id) DO UPDATE SET "
            "escalation_level=excluded.escalation_level, model=excluded.model",
            (old.node_id, level, model, old.created_at),
        )
        if not is_store_connection(conn) and conn.execute(
            "SELECT 1 FROM sqlite_master WHERE type='table' AND name='nodes_fts'"
        ).fetchone():
            conn.execute("INSERT INTO nodes_fts(nodes_fts, rowid, summary) VALUES('delete', ?, ?)",
                         (old.node_id, old.summary))
            conn.execute("INSERT INTO nodes_fts(rowid, summary) VALUES(?, ?)", (old.node_id, new.summary))
        VectorStore.purge_embedding_batch_on_connection(conn, [old.node_id])
        return True

    with dag._db_lock:
        return _run_sqlite_write_with_snapshot_retry(conn, update, operation_name="taste recondense")


def _refresh_prefix(
    engine: LCMEngine, messages, spans, replacements: dict[int, tuple[SummaryNode, SummaryNode]],
):
    """Replace exact engine-emitted node parts while preserving any carried raw suffix."""
    from .engine import _summary_part_text

    if not replacements:
        return messages
    refreshed = []
    for message, (entry, span) in zip(messages, spans):
        content = message.get("content")
        if not span or not isinstance(content, str):
            refreshed.append(message)
            continue
        new_span = span
        for old, new in replacements.values():
            if old.session_id == engine._session_id:
                old_part, new_part = _summary_part_text(old), _summary_part_text(new)
                new_span = re.sub(
                    r"(^|\n\n---\n\n)" + re.escape(old_part) + r"(?=$|\n\n---\n\n)",
                    lambda match: match.group(1) + new_part,
                    new_span,
                )
        if new_span == span or not content.startswith(span):
            refreshed.append(message)
            continue
        updated = {**message, "content": new_span + content[len(span):]}
        refreshed.append(updated)
        engine._pending_emission_candidates.append({
            "kind": entry.kind or ("carrier" if len(span) < len(content) else "summary"),
            "span": new_span, "row": updated, "full_identity": _emission_identity(updated),
            "retained_source": entry.retained_source,
        })
    return refreshed if any(a is not b for a, b in zip(messages, refreshed)) else messages


def recondense_forgotten_tastes(engine: LCMEngine, messages):
    """Apply eligible owner tombstones at an actual compression boundary."""
    dag = engine._dag
    if not engine._hermes_home or not engine._session_id or dag.connection is None:
        return messages
    targets = pending_targets(dag, Path(engine._hermes_home))
    if not targets:
        return messages
    # Read only the two naming surfaces before loading sources. Condensation
    # always points down, so rewritten leaves are available to their ancestors.
    # A different session may still hold its previous emitted prefix. Its own
    # compression must verify and consume that prefix before replacing its nodes.
    candidates = dag.connection.execute(
        "SELECT node_id, summary, expand_hint FROM summary_nodes WHERE session_id=? ORDER BY depth, node_id",
        (engine._session_id,),
    ).fetchall()
    occurrences, _ = engine._replay_occurrences(messages)
    spans = []
    for message, (entry, _) in zip(messages, occurrences):
        content = message.get("content")
        end = (
            engine._verified_lcm_summary_prefix_end(content)
            if isinstance(content, str) and message.get("role") in {"user", "assistant"} else None
        )
        spans.append((entry, entry.generated_span or (content[:end] if end is not None else "")))
    replacements: dict[int, tuple[SummaryNode, SummaryNode]] = {}
    for node_id, summary, hint in candidates:
        if not (names_target(summary, targets) or names_target(hint or "", targets)):
            continue
        old = dag.get_node(node_id)
        if old is None or (old.summary, old.expand_hint) != (summary, hint or ""):
            continue
        rewritten = _recondense_node(engine, old, targets)
        if _publish(engine, old, *rewritten):
            replacements[node_id] = old, rewritten[0]
        elif old.session_id == engine._session_id:
            # A concurrent repair may have run the same work while our model
            # call was in flight. Do not return its superseded snapshot.
            current = dag.get_node(node_id)
            if current is None or current.session_id != old.session_id:
                replacements[node_id] = old, _fallback(old, targets)
            elif not (names_target(current.summary, targets) or names_target(current.expand_hint, targets)):
                replacements[node_id] = old, current
            else:
                fallback = _fallback(current, targets)
                _publish(engine, current, fallback, 3, "deterministic")
                replacements[node_id] = old, fallback
    return _refresh_prefix(engine, messages, spans, replacements)
