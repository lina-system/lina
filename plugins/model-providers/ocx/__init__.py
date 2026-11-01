"""OCX provider profile: every model call goes through the local OCX (opencodex) proxy.

OCX serves an OpenAI-compatible data plane (``/v1/chat/completions``, ``/v1/models``) on
``127.0.0.1:10100`` by default and routes each ``<provider>/<model>`` id to the upstream it names,
so one profile stands in for the per-vendor profiles. The wire shape follows OCX's own Hermes
export (``ocx export --client hermes``): chat completions, Bearer admission key, and a
``session-id`` header carrying the conversation key for sticky routing.
"""

from typing import Any

from agent.portal_tags import get_affinity_scope, get_conversation_context
from agent.reasoning_effort import OPENAI_COMPAT_WIRE_EFFORTS, clamp_effort
from agent.transports.codex import _cache_scope_from_session_id
from providers import register_provider
from providers.base import ProviderProfile

OCX_DEFAULT_BASE_URL = "http://127.0.0.1:10100/v1"
SESSION_AFFINITY_HEADER = "session-id"


class OcxProfile(ProviderProfile):
    def supported_reasoning_efforts(self, model: str | None) -> tuple[str, ...]:
        return OPENAI_COMPAT_WIRE_EFFORTS

    def build_api_kwargs_extras(
        self, *, reasoning_config: dict | None = None, session_id: str | None = None, **context: Any
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        """Top-level ``reasoning_effort`` (OCX translates it per upstream) and the ``session-id``
        header. The key is the declared routing scope, then the ambient conversation, then the
        explicit session id, so auxiliary calls stay on their conversation's route."""
        top_level: dict[str, Any] = {}
        if isinstance(reasoning_config, dict):
            effort = (reasoning_config.get("effort") or "").strip().lower()
            if effort == "none" or reasoning_config.get("enabled", True) is False:
                top_level["reasoning_effort"] = "none"
            elif effort:
                top_level["reasoning_effort"] = clamp_effort(effort, OPENAI_COMPAT_WIRE_EFFORTS)
        affinity = _cache_scope_from_session_id(get_affinity_scope() or get_conversation_context() or session_id)
        if affinity:
            top_level["extra_headers"] = {SESSION_AFFINITY_HEADER: affinity}
        return {}, top_level


ocx = OcxProfile(
    name="ocx",
    aliases=("opencodex",),
    display_name="OCX",
    description="OCX — the local model proxy every model call goes through",
    env_vars=("OCX_API_KEY", "OCX_BASE_URL"),
    base_url=OCX_DEFAULT_BASE_URL,
    supports_vision=True,
)

register_provider(ocx)
