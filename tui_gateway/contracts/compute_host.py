"""Line-delimited JSON frames between the gateway supervisor and compute host."""

from __future__ import annotations

from typing import Annotated, Literal
from urllib.parse import parse_qsl, urlsplit

from pydantic import BaseModel, ConfigDict, Discriminator, Field, JsonValue, Tag, field_validator
from pydantic_core import PydanticCustomError

from .common import TranscriptMessage

JsonObject = dict[str, JsonValue]
RequestId = str | int


class _WireModel(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True, hide_input_in_errors=True)


def _credential_key(key: str) -> bool:
    normalized = "".join(char for char in key.lower() if char.isalnum())
    return normalized.endswith(("apikey", "authorization", "cookie", "password", "secret")) or normalized in {
        "accesskey", "accesstoken", "auth", "authtoken", "bearertoken", "credentials",
        "defaultheaders", "extraheaders", "headers", "privatekey", "refreshtoken", "token",
    }


def _contains_credential_field(value: JsonValue) -> bool:
    if isinstance(value, dict):
        return any(_credential_key(key) or _contains_credential_field(item) for key, item in value.items())
    if isinstance(value, list):
        return any(_contains_credential_field(item) for item in value)
    return False


class TurnPlan(_WireModel):
    """Resolved model request configuration. The child supplies provider authentication."""

    system: str
    tools: list[JsonObject]
    model: str
    base_url: str = Field(description="Provider base URL without embedded credentials.")
    params: JsonObject = Field(description="JSON request options without credentials or transport headers.")
    stream: bool
    api_max_retries: int = Field(default=1, ge=1, description="Total logical attempts per model round.")
    max_iterations: int = Field(default=500, description="Effective iteration budget of the planning agent.")
    read_file_hints: bool = False

    @field_validator("base_url")
    @classmethod
    def _credential_free_url(cls, value: str) -> str:
        url = urlsplit(value)
        if url.username is not None or any(_credential_key(key) for key, _ in parse_qsl(url.query)):
            raise PydanticCustomError("turn_plan_credentials", "base_url must be credential-free")
        return value

    @field_validator("params")
    @classmethod
    def _credential_free_params(cls, value: JsonObject) -> JsonObject:
        if _contains_credential_field(value):
            raise PydanticCustomError("turn_plan_credentials", "params must be credential-free")
        return value


class ActiveSessionLease(_WireModel):
    lease_id: str
    session_id: str


class TurnStart(_WireModel):
    type: Literal["turn.start"]
    sid: str
    request_id: RequestId | None = None
    session_key: str | None = None
    turn_id: str | None = None
    text: JsonValue = None
    prompt: JsonValue = None
    history: list[JsonObject] | None = None
    history_version: int | None = None
    cols: int | None = None
    cwd: str | None = None
    terminal_backend: Literal["local"] | None = None
    explicit_cwd: bool | None = None
    context_cwd_is_launch_artifact: bool | None = None
    profile_home: str | None = None
    model_override: JsonObject | None = None
    pending_model_switch: JsonObject | None = None
    reasoning_config_override: JsonObject | None = None
    service_tier_override: str | None = None
    source: str | None = None
    attached_images: list[str] | None = None
    auth_user_id: str | None = None
    queued_prompt_generation: int | None = None
    display_kind: str | None = None
    display_metadata: JsonObject | None = None
    active_session_lease: ActiveSessionLease | None = None
    turn_plan: TurnPlan | None = None
    messages: list[JsonObject] | None = None


class _SessionRequest(_WireModel):
    sid: str
    request_id: RequestId | None = None


class Interrupt(_SessionRequest):
    type: Literal["interrupt"]


class Respond(_SessionRequest):
    type: Literal["respond"]
    # Wrapped response, batch clarify lock, cancellation, or legacy bare JSON-RPC response.
    params: JsonObject


class ReloadMcp(_SessionRequest):
    type: Literal["reload_mcp"]
    # HostSupervisor.reload_mcp sends this frame via the control path.
    route_name: Literal["reload.mcp"] | None = None


class Control(_SessionRequest):
    type: Literal["control"]
    route_name: str
    command: str | None = None


class Shutdown(_WireModel):
    type: Literal["shutdown"]
    request_id: RequestId | None = None


class FileIdentity(_WireModel):
    dev: int
    ino: int
    size: int
    mtime_ns: int
    ctime_ns: int


class _PolicyFrame(_WireModel):
    """Private per-read IPC. Keep these frames out of gateway events, logs, and storage."""

    sid: str
    turn_id: str
    request_id: RequestId


class _PolicyRequest(_PolicyFrame):
    type: Literal["read_file.policy"]
    path: str = Field(repr=False)


class ReadFileAuthorize(_PolicyRequest):
    op: Literal["authorize"]


class ReadFileHintArgs(_WireModel):
    path: str | None = Field(default=None, repr=False)
    file_path: str | None = Field(default=None, repr=False)
    workdir: str | None = Field(default=None, repr=False)


class ReadFileRedact(_PolicyRequest):
    op: Literal["redact"]
    resolved: str = Field(repr=False)
    identity: FileIdentity
    content: str = Field(repr=False)
    hint_args: ReadFileHintArgs | None = Field(default=None, repr=False)


class ReadFileHints(_PolicyFrame):
    type: Literal["read_file.policy"]
    op: Literal["hints"]
    hint_args: ReadFileHintArgs = Field(repr=False)


ReadFilePolicy = Annotated[ReadFileAuthorize | ReadFileRedact | ReadFileHints, Field(discriminator="op")]


class IterationSummary(_PolicyFrame):
    type: Literal["iteration.summary"]
    messages: list[JsonObject] = Field(repr=False)


class IterationSummaryAck(_PolicyFrame):
    type: Literal["iteration.summary.ack"]
    ok: bool
    body: JsonObject | None = Field(default=None, repr=False)


class ProviderError(_WireModel):
    """Private, bounded fields used for retry policy and terminal presentation."""

    status_code: int = Field(ge=400, le=599)
    message: str = Field(max_length=512, pattern=r"^[^\x00-\x1f\x7f]*$", repr=False)
    code: str | None = Field(default=None, max_length=128, repr=False)
    retry_after: str | None = Field(default=None, max_length=128, repr=False)
    body_retry_after: str | None = Field(default=None, max_length=128, repr=False)
    attempts: int = Field(ge=1, description="Logical attempts, excluding diagnostic HTTP probes.")


class TransportError(_WireModel):
    transport: Literal["connection"]
    attempts: int = Field(ge=1)


class ModelRetry(_PolicyFrame):
    type: Literal["model.retry"]
    failure: ProviderError | TransportError = Field(repr=False)


class ModelRetryAck(_PolicyFrame):
    type: Literal["model.retry.ack"]
    retry_delay_s: float | None = Field(default=None, ge=0, le=600, allow_inf_nan=False)


class ContextResponseUsage(_WireModel):
    usage: JsonObject | None = Field(repr=False)
    api_duration_s: float = Field(default=0.0, ge=0.0, allow_inf_nan=False)
    priced_prefix_count: int = Field(ge=1)
    response_index: int | None = Field(default=None, ge=1)


class EmptyAttempt(_WireModel):
    finish_reason: str
    retry_count: int = Field(ge=0, le=3)
    observed_generation: bool


class ContextPreflight(_PolicyFrame, ContextResponseUsage):
    type: Literal["context.preflight"]
    messages: list[JsonObject] = Field(repr=False)
    summary: bool
    empty_retry: Literal[True] | None = None
    empty_attempt: EmptyAttempt | None = None


class ContextPreflightAck(_PolicyFrame):
    type: Literal["context.preflight.ack"]
    decision: Literal["allow", "pressure", "unavailable"]
    retry_delay_s: float | None = Field(default=None, ge=0, le=90, allow_inf_nan=False)


class ReadFileAuthorizeAck(_PolicyFrame):
    type: Literal["read_file.policy.ack"]
    ok: Literal[True]
    resolved: str = Field(repr=False)
    display_path: str = Field(repr=False)
    identity: FileIdentity
    max_chars: int
    max_line_length: int
    max_lines: int


class ReadFileRedactAck(_PolicyFrame):
    type: Literal["read_file.policy.ack"]
    ok: Literal[True]
    content: str = Field(repr=False)
    hints: str | None = Field(default=None, repr=False)


class ReadFileHintsAck(_PolicyFrame):
    type: Literal["read_file.policy.ack"]
    ok: Literal[True]
    hints: str = Field(repr=False)


class ReadFileDeniedAck(_PolicyFrame):
    type: Literal["read_file.policy.ack"]
    ok: Literal[False]
    # The handler echoes missing correlation fields when it rejects malformed input.
    sid: str | None
    turn_id: str | None
    request_id: RequestId | None
    error: str | None = Field(default=None, repr=False)
    result: JsonObject | None = Field(default=None, repr=False)


def _policy_ack_kind(frame: JsonObject | _PolicyFrame) -> str:
    """Distinguish successful replies by the payload of the operation."""
    if isinstance(frame, dict):
        if frame.get("ok") is False:
            return "denied"
        if "resolved" in frame:
            return "authorize"
        return "redact" if "content" in frame else "hints"
    if isinstance(frame, ReadFileDeniedAck):
        return "denied"
    if isinstance(frame, ReadFileAuthorizeAck):
        return "authorize"
    return "redact" if isinstance(frame, ReadFileRedactAck) else "hints"


ReadFilePolicyAck = Annotated[
    Annotated[ReadFileAuthorizeAck, Tag("authorize")]
    | Annotated[ReadFileRedactAck, Tag("redact")]
    | Annotated[ReadFileHintsAck, Tag("hints")]
    | Annotated[ReadFileDeniedAck, Tag("denied")],
    Discriminator(_policy_ack_kind),
]


ComputeHostRequest = Annotated[
    TurnStart | Interrupt | Respond | ReloadMcp | Control | Shutdown | ReadFilePolicyAck
    | IterationSummaryAck | ContextPreflightAck | ModelRetryAck,
    Field(discriminator="type"),
]


class _ChildFrame(_WireModel):
    host_ns: int


class _SessionReply(_ChildFrame):
    sid: str
    request_id: RequestId | None


class Hello(_ChildFrame):
    type: Literal["hello"]
    host_pid: int
    boot_id: str
    build_sha: str
    cwd: str
    hermes_home: str


class Heartbeat(_ChildFrame):
    type: Literal["hb"]
    active_turns: int
    progress_counter: int
    rss_mb: float


class Orphan(_ChildFrame):
    type: Literal["orphan"]
    old_ppid: int
    ppid: int


class Rpc(_ChildFrame):
    type: Literal["rpc"]
    sid: str
    message: JsonObject


class _HistoryMetadata(_WireModel):
    session_key: str | None = None
    history_version: int | None = None
    message_count: int | None = None
    session_info: JsonObject | None = None


class TurnStarted(_SessionReply):
    type: Literal["turn.started"]
    request_id: str
    started_ns: int


class TurnEnd(_SessionReply, _HistoryMetadata):
    type: Literal["turn.end"]
    request_id: str
    interrupted: bool
    ended_ns: int
    session_info_emitted: bool | None = None
    turn_id: str | None = None
    messages: list[JsonObject] | None = None
    new_messages: list[JsonObject] | None = None
    provider_error: ProviderError | TransportError | None = None
    context_response: ContextResponseUsage | None = None
    context_failure: Literal["context_pressure", "context_preflight_unavailable"] | None = None
    failure_reason: Literal["truncated", "content_policy_blocked"] | None = None
    failure_retryable: bool | None = None
    max_iterations_reached: Literal[True] | None = None
    summary_outcome: Literal["success", "empty", "error"] | None = None
    empty_response_exhausted: Literal["empty", "reasoning"] | None = None


class TurnError(_SessionReply):
    type: Literal["turn.error"]
    request_id: str
    message: str
    reason: str | None = None


class InterruptAck(_SessionReply):
    type: Literal["interrupt.ack"]
    applied: bool
    applied_ns: int | None = None
    message: str | None = None


class _RpcReply(_SessionReply):
    response: JsonObject


class RespondAck(_RpcReply):
    type: Literal["respond.ack"]


class RespondError(_SessionReply):
    type: Literal["respond.error"]
    message: str


class ReloadMcpAck(_RpcReply):
    type: Literal["reload_mcp.ack"]


class ControlAck(_SessionReply, _HistoryMetadata):
    type: Literal["control.ack"]
    route_name: str
    result: JsonObject | None = None
    output: str | None = None
    messages: list[TranscriptMessage] | None = None


class ControlError(_SessionReply):
    type: Literal["control.error"]
    message: str
    code: int | None = None


class ShutdownAck(_ChildFrame):
    type: Literal["shutdown.ack"]
    request_id: RequestId | None


class Error(_ChildFrame):
    type: Literal["error"]
    # Unknown frame errors echo even an unrecognized request's correlation value.
    request_id: JsonValue = None
    message: str


ComputeHostResponse = Annotated[
    Hello | Heartbeat | Orphan | Rpc | TurnStarted | TurnEnd | TurnError | InterruptAck
    | RespondAck | RespondError | ReloadMcpAck | ControlAck | ControlError | ShutdownAck | Error
    | ReadFilePolicy | IterationSummary | ContextPreflight | ModelRetry,
    Field(discriminator="type"),
]

ComputeHostFrame = Annotated[ComputeHostRequest | ComputeHostResponse, Field(discriminator="type")]
