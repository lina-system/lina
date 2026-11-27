"""Construct the Python-side planning state for a deferred native turn."""

from __future__ import annotations

import os
from datetime import datetime

from agent.chat_completion_helpers import build_api_kwargs
from agent.client_lifecycle import ClientLifecycleMixin
from agent.reasoning_params import ReasoningParamsMixin
from agent.stream_delivery import StreamDeliveryMixin
from agent.vision_message_prep import VisionMessagePrepMixin
from run_agent import AIAgent
from tui_gateway.rust_plan_accounting import RustPlanAccounting, init_plan_accounting
from tui_gateway.rust_plan_prompt import RustPlanPrompt, init_plan_prompt


class RustPlanState(RustPlanPrompt, RustPlanAccounting):
    """Inputs for production request assembly, stream policy and durable settlement."""

    _force_ascii_payload = False
    base_url = AIAgent.base_url
    _hostname_for = AIAgent._hostname_for
    _is_direct_openai_url = AIAgent._is_direct_openai_url
    _is_azure_openai_url = AIAgent._is_azure_openai_url
    _is_openrouter_url = AIAgent._is_openrouter_url
    _is_codex_backend = AIAgent._is_codex_backend
    _max_tokens_param = AIAgent._max_tokens_param
    _resolved_api_call_timeout = AIAgent._resolved_api_call_timeout
    _apply_user_default_headers = ClientLifecycleMixin._apply_user_default_headers
    _has_stream_consumers = StreamDeliveryMixin._has_stream_consumers
    _get_transport = VisionMessagePrepMixin._get_transport
    _is_qwen_portal = VisionMessagePrepMixin._is_qwen_portal
    _supports_reasoning_extra_body = ReasoningParamsMixin._supports_reasoning_extra_body
    _reasoning_endpoint_profile = ReasoningParamsMixin._reasoning_endpoint_profile
    _endpoint_reasoning_payload = ReasoningParamsMixin._endpoint_reasoning_payload
    _build_api_kwargs = build_api_kwargs

    def _prepare_messages_for_non_vision_model(self, messages):
        # The vision fallback can invoke an auxiliary model and create temporary files.
        # Refuse even an image recovered from an encoded historical row.
        if any(VisionMessagePrepMixin._content_has_image_parts(m.get("content")) for m in messages):
            raise ValueError("Native planning requires a text transcript")
        return messages


def _plain_chat_provider(provider: str) -> bool:
    from providers import ProviderProfile, get_provider_profile

    if provider == "custom" or provider.startswith("custom:"):
        return True
    profile = get_provider_profile(provider)
    return bool(profile and profile.api_mode == "chat_completions" and profile.auth_type == "api_key"
                and type(profile).create_client is ProviderProfile.create_client
                and not profile.refresh_credential and not profile.classify_api_error
                and provider not in {"moa", "lmstudio", "openrouter"})


def _plain_route_requested(server, overrides) -> bool:
    from hermes_cli.runtime_provider import resolve_requested_provider

    model = overrides.get("model_override")
    provider = overrides.get("provider_override")
    if isinstance(model, dict) and model.get("model"):
        provider = model.get("provider") or provider
    elif not provider:
        _, provider = server._resolve_startup_runtime()
    requested = resolve_requested_provider(provider)
    return _plain_chat_provider(requested)


def _fixed_credential(runtime: dict) -> bool:
    """The resolver has already selected a key. Never select or refresh it again."""
    from agent.credential_pool import (
        AUTH_TYPE_API_KEY, STATUS_OK, CredentialPool, credential_pool_entry_serves_endpoint,
        credential_pool_matches_provider,
    )

    pool = runtime.get("credential_pool")
    if pool is None:
        return True
    if type(pool) is not CredentialPool:
        return False
    entries = pool.entries()
    if len(entries) != 1:
        return False
    entry = entries[0]
    return (
        pool.current() is entry and entry.auth_type == AUTH_TYPE_API_KEY
        and entry.last_status in (None, STATUS_OK) and not entry.model_cooldowns
        and not any((entry.refresh_token, entry.expires_at, entry.expires_at_ms,
                     entry.agent_key, entry.agent_key_expires_at))
        and entry.runtime_api_key == runtime.get("api_key")
        and credential_pool_entry_serves_endpoint(entry, runtime["base_url"])
        and credential_pool_matches_provider(pool, runtime["provider"], base_url=runtime["base_url"])
    )


def _project_route(state, model, runtime, cfg) -> bool:
    """Require a plain bearer-authenticated chat transport before touching tool state."""
    from agent import agent_init
    from agent.agent_runtime_helpers import _ensure_route_client_headers, _profile_for_base_url
    from agent.auxiliary_client import _to_openai_base_url
    from agent.codex_headers import is_official_codex_base_url
    from agent.gemini_native_adapter import is_native_gemini_base_url
    from agent.native_compaction import resolve_native_compaction_capabilities
    from agent.reasoning_promotion import answer_in_reasoning_capability
    from hermes_cli.config import get_compatible_custom_providers
    from hermes_cli.model_normalize import _AGGREGATOR_PROVIDERS, normalize_model_for_provider
    from hermes_cli.timeouts import get_provider_request_timeout
    from providers import ProviderProfile, get_provider_profile, profile_for_host
    from tui_gateway.rust_turn_plan import _model_environment

    provider = runtime.get("provider") or ""
    if (not model or not runtime.get("base_url") or not _plain_chat_provider(provider)
            or runtime.get("api_mode") != "chat_completions" or not _fixed_credential(runtime)
            or any(runtime.get(k) for k in ("command", "args", "_fallback_notice", "extra_headers"))):
        return False
    state.provider = provider
    state.requested_provider = runtime.get("requested_provider") or state.provider
    state.model = model if provider in _AGGREGATOR_PROVIDERS else normalize_model_for_provider(model, provider)
    state.api_mode = runtime["api_mode"]
    state.base_url = runtime.get("base_url") or ""
    state.api_key = runtime.get("api_key")
    if not isinstance(state.api_key, str) or not state.api_key or not state.base_url:
        return False
    host_profile = profile_for_host(state.base_url)
    profile = get_provider_profile(provider)
    if (_to_openai_base_url(state.base_url) != state.base_url.rstrip("/")
            or (host_profile is not None and host_profile is not profile) or state._is_openrouter_url()
            or state._is_qwen_portal() or is_native_gemini_base_url(state.base_url)
            or is_official_codex_base_url(state.base_url)
            or "bedrock-mantle." in state.base_url):
        return False
    profile = profile or _profile_for_base_url(state.base_url)
    if profile is not None and (
        type(profile).create_client is not ProviderProfile.create_client
        or profile.build_client_kwargs_extras(base_url=state.base_url)
    ):
        return False
    state._custom_providers = get_compatible_custom_providers(cfg)
    if answer_in_reasoning_capability(state):
        return False
    timeout = get_provider_request_timeout(state.provider, state.model)
    state._client_kwargs = agent_init._explicit_client_kwargs(state, state.api_key, state.base_url, timeout)
    agent_init._apply_openai_header_policy(state, state._client_kwargs)
    _ensure_route_client_headers(state._client_kwargs, state.provider)
    if _model_environment(state) is None:
        return False
    state.runtime_capabilities = resolve_native_compaction_capabilities(
        model=state.model, provider=state.provider, base_url=state.base_url,
    )
    state.request_overrides = dict(runtime.get("request_overrides") or {})
    agent_init._merge_custom_provider_extra_body(state, state._custom_providers)
    return True


def _project_request_policy(state, server, overrides, cfg) -> bool:
    from agent.agent_init import _apply_display_config, _cfg_dict, _normalize_run_budget_seconds
    from agent.fast_mode import BOUNDED_MODES

    reasoning = overrides.get("reasoning_config_override")
    state.reasoning_config = reasoning if reasoning is not None else server._load_reasoning_config(state.model)
    tier = overrides.get("service_tier_override")
    state.service_tier = tier if tier is not None else server._load_service_tier()
    section = _cfg_dict(cfg, "agent")
    state.run_budget_seconds = _normalize_run_budget_seconds(section.get("run_budget_seconds"))
    if state.service_tier in BOUNDED_MODES or state.run_budget_seconds is not None:
        return False
    try:
        state._api_max_retries = max(1, int(section.get("api_max_retries", 3)))
    except (TypeError, ValueError):
        state._api_max_retries = 3
    routing = server._load_provider_routing()
    state.providers_allowed = routing.get("only")
    state.providers_ignored = routing.get("ignore")
    state.providers_order = routing.get("order")
    state.provider_sort = routing.get("sort")
    state.provider_require_parameters = routing.get("require_parameters", False)
    state.provider_data_collection = routing.get("data_collection")
    state.openrouter_min_coding_score = None
    state.max_tokens = None
    _apply_display_config(state, cfg, state.platform)
    return True


def build_plan_state(sid: str, session: dict, db) -> RustPlanState | None:
    """Borrow the caller's profile scope and DB. Return None to use normal agent construction."""
    from agent.agent_init import (
        _build_context_engine, _configure_ollama_num_ctx, _enforce_minimum_context, _parse_compression_config,
        _setup_logging,
    )
    from agent.agent_init_params import _CALLBACK_PARAMS
    from agent.onboarding_session import onboarding_toolsets
    from hermes_cli.config import load_config_readonly
    from tui_gateway import server
    from tui_gateway.rust_turn_plan import _compatible_agent
    from utils import is_truthy_value

    if db is None or session.get("agent") is not None or not session.get("session_key"):
        return None
    overrides = server._deferred_build_agent_kwargs(session, db)
    if server._load_fallback_model() or not _plain_route_requested(server, overrides):
        return None
    model, runtime = server._resolve_agent_model_runtime(
        overrides.get("model_override"), overrides.get("provider_override"),
    )
    cfg = load_config_readonly()
    state = RustPlanState()
    if not _project_route(state, model, runtime, cfg):
        return None
    _setup_logging(state)
    factory_cfg = server._load_cfg()
    state.platform = server._resolve_agent_platform(overrides.get("platform_override"))
    state.session_id = overrides.get("session_id") or session["session_key"]
    state.session_start = datetime.now()
    state.cwd = overrides["cwd_override"]
    state._user_id = server._session_auth_user_id(session)
    state.max_iterations = server._cfg_max_turns(factory_cfg, 500)
    if not _project_request_policy(state, server, overrides, cfg):
        return None
    callbacks = server._agent_cbs(sid)
    for name in _CALLBACK_PARAMS:
        setattr(state, name, callbacks.get(name))
    enabled = onboarding_toolsets(server._load_enabled_toolsets(state.platform), db, state.session_id)
    init_plan_accounting(state, cfg=cfg, session=session, db=db)
    if not init_plan_prompt(
        state, cfg=cfg, session=session, enabled_toolsets=enabled,
        disabled_toolsets=server._load_disabled_toolsets(),
        system_prompt=server._startup_system_prompt(factory_cfg, state.session_id),
        ignore_rules=is_truthy_value(os.environ.get("HERMES_IGNORE_RULES")),
    ):
        return None
    if state.context_compressor is None:
        # Prompt initialization has already ruled out a plugin context engine.
        # Let the production compressor discover the window with this route's
        # credentials; a discovered value must not become a configuration pin.
        _build_context_engine(
            state, cfg, _parse_compression_config(state, cfg), state._custom_providers,
            state._config_context_length, db,
        )
    if not _compatible_agent(state):
        return None
    _configure_ollama_num_ctx(state, cfg.get("model"), state._config_context_length)
    if state._ollama_num_ctx:
        # Native chat bodies cannot carry the provider-specific num_ctx option.
        return None
    try:
        _enforce_minimum_context(state)
    except ValueError:
        return None
    return state
