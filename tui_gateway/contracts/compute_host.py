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


class ReadFileRedact(_PolicyRequest):
    op: Literal["redact"]
    resolved: str = Field(repr=False)
    identity: FileIdentity
    content: str = Field(repr=False)


ReadFilePolicy = Annotated[ReadFileAuthorize | ReadFileRedact, Field(discriminator="op")]


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
    """The reply omits the operation tag. A successful authorization carries a resolved path."""
    if isinstance(frame, dict):
        if frame.get("ok") is False:
            return "denied"
        return "authorize" if "resolved" in frame else "redact"
    if isinstance(frame, ReadFileDeniedAck):
        return "denied"
    return "authorize" if isinstance(frame, ReadFileAuthorizeAck) else "redact"


ReadFilePolicyAck = Annotated[
    Annotated[ReadFileAuthorizeAck, Tag("authorize")]
    | Annotated[ReadFileRedactAck, Tag("redact")]
    | Annotated[ReadFileDeniedAck, Tag("denied")],
    Discriminator(_policy_ack_kind),
]


ComputeHostRequest = Annotated[
    TurnStart | Interrupt | Respond | ReloadMcp | Control | Shutdown | ReadFilePolicyAck,
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
    | ReadFilePolicy,
    Field(discriminator="type"),
]

ComputeHostFrame = Annotated[ComputeHostRequest | ComputeHostResponse, Field(discriminator="type")]
