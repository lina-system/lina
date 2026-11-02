"""ACP protocol payloads and permission bridge for neutral edit proposals."""

from __future__ import annotations

import asyncio
import logging
from itertools import count
from typing import Callable

from tools.edit_approval import EditApprovalRequester, EditProposal, should_auto_approve_edit

logger = logging.getLogger(__name__)
_PERMISSION_REQUEST_IDS = count(1)


def build_acp_edit_tool_call(proposal: EditProposal):
    """Build the ToolCallUpdate payload for ACP request_permission."""
    import acp
    from agent.redact import redact_approval_display

    # The title is display text; the diff keeps the raw path the client opens.
    return acp.update_tool_call(
        f"edit-approval-{next(_PERMISSION_REQUEST_IDS)}", title=f"Approve edit: {redact_approval_display(proposal.path)}",
        kind="edit",
        status="pending",
        content=[acp.tool_diff_content(path=proposal.path, old_text=proposal.old_text, new_text=proposal.new_text)],
        raw_input={"tool": proposal.tool_name, "arguments": proposal.arguments},
    )


def make_acp_edit_approval_requester(
    request_permission_fn: Callable, loop: asyncio.AbstractEventLoop, session_id: str,
    timeout: float | None = None, auto_approve_getter: Callable[[], tuple[str, str | None]] | None = None,
    send_update: Callable[[object], None] | None = None,
) -> EditApprovalRequester:
    """Return a sync requester that bridges edit proposals to ACP permissions."""

    def _requester(proposal: EditProposal) -> bool:
        from acp.schema import PermissionOption
        from acp_adapter.permissions import await_permission, resolve_permission_timeout

        if auto_approve_getter is not None:
            try:
                policy, cwd = auto_approve_getter()
                if should_auto_approve_edit(proposal, policy, cwd):
                    logger.info("Auto-approved ACP edit under policy %s: %s", policy, proposal.path)
                    return True
            except Exception:
                logger.debug("ACP edit auto-approval policy check failed", exc_info=True)

        response, _timed_out = await_permission(
            request_permission_fn, loop, session_id, tool_call=build_acp_edit_tool_call(proposal),
            options=[PermissionOption(option_id="allow_once", kind="allow_once", name="Allow edit"),
                     PermissionOption(option_id="deny", kind="reject_once", name="Deny")],
            timeout=resolve_permission_timeout(timeout), what="Edit approval request", send_update=send_update,
        )
        outcome = getattr(response, "outcome", None)
        return getattr(outcome, "outcome", None) == "selected" and getattr(outcome, "option_id", None) == "allow_once"

    return _requester
