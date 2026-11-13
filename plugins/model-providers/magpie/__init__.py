"""magpie provider profile for the local OpenAI-compatible model gateway.

The gateway serves chat completions and model discovery under ``/v1`` and routes each
``<provider>/<model>`` id upstream. Bearer admission keys authenticate requests; the
``session-id`` header carries the conversation key for sticky routing.
"""

from typing import Any
from urllib.parse import urlparse

from agent.portal_tags import get_affinity_scope, get_conversation_context
from agent.reasoning_effort import OPENAI_COMPAT_WIRE_EFFORTS, clamp_effort
from agent.transports.codex import _cache_scope_from_session_id
from providers import register_provider
from providers.base import ProviderProfile

MAGPIE_DEFAULT_BASE_URL = "http://127.0.0.1:3425/v1"
SESSION_AFFINITY_HEADER = "session-id"


class MagpieProfile(ProviderProfile):
    def supported_reasoning_efforts(self, model: str | None) -> tuple[str, ...]:
        return OPENAI_COMPAT_WIRE_EFFORTS

    def default_reasoning_config(self, model: str | None = None) -> dict | None:
        """Unset ``agent.reasoning_effort`` -> ``medium``: a relayed reasoning model's own default
        can be its ceiling (3x the tokens and latency of medium)."""
        return {"enabled": True, "effort": "medium"}

    def build_api_kwargs_extras(
        self, *, reasoning_config: dict | None = None, session_id: str | None = None, **context: Any
    ) -> tuple[dict[str, Any], dict[str, Any]]:
        """Top-level ``reasoning_effort`` (magpie translates it per upstream) and the ``session-id``
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


magpie = MagpieProfile(
    name="magpie",
    display_name="magpie",
    description="magpie — the local model gateway every model call goes through",
    env_vars=("MAGPIE_API_KEY", "MAGPIE_BASE_URL"),
    base_url=MAGPIE_DEFAULT_BASE_URL,
    # URL->provider inference matches this against the host:port, so only magpie's own port maps to
    # magpie; a bare loopback host would claim every local server (Ollama, llama.cpp, LM Studio).
    hostname=urlparse(MAGPIE_DEFAULT_BASE_URL).netloc,
    supports_vision=True,
)

register_provider(magpie)
