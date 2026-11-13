"""``outbound.allow`` at cron delivery (lina#638): each resolved target is checked before any lane
touches it, and a refused one is booked as ``last_delivery_refused`` instead of delivered."""

from __future__ import annotations

import logging
from typing import Optional

from agent.outbound_lock import check_outbound, target_label

logger = logging.getLogger(__name__)


def target_refusal(target: dict) -> Optional[str]:
    """The refusal for one resolved delivery target (home channel already resolved), or None. The
    job's own local chat and own Bot Chat stay on this agent and are never refused."""
    from cron.scheduler_delivery import BOT_CHAT_PLATFORM
    platform, chat_id = target["platform"], target.get("chat_id")
    if platform == "local" or (platform == BOT_CHAT_PLATFORM and not chat_id):
        return None
    if platform == BOT_CHAT_PLATFORM:
        return check_outbound("agent", chat_id, door="cron delivery")
    return check_outbound(platform, chat_id, target.get("thread_id"), door="cron delivery")


def refused_label(target: dict) -> str:
    return target_label(target["platform"], target.get("chat_id"), target.get("thread_id"))


def record_refused(job: dict, refused: list) -> None:
    """Persist ``last_delivery_refused`` (the refused targets of the latest delivery, or None)."""
    value = list(refused) or None
    if (job.get("last_delivery_refused") or None) == value:
        return
    job["last_delivery_refused"] = value
    try:
        from cron.jobs import update_job
        update_job(job["id"], {"last_delivery_refused": value})
    except Exception as exc:  # health: allow BLE001 -- bookkeeping must not fail the delivery; logged with traceback
        logger.warning("Job '%s': could not record refused delivery targets: %s", job.get("id"), exc, exc_info=True)
