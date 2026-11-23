"""The signed-in LINA dashboard's view of its agents' approval queues."""

from dataclasses import dataclass, field
from typing import Annotated, Literal

import httpx
from fastapi import APIRouter, Depends, HTTPException, Request
from pydantic import BaseModel, ConfigDict, Field, FiniteFloat
from starlette.concurrency import run_in_threadpool

from gateway.owner_identity import principal_subject
from hermes_cli import lina_talk
from hermes_cli.dashboard_auth.base import Session
from hermes_cli.work import env

router = APIRouter()

_ContentVersion = Annotated[str, Field(pattern=r"^[0-9a-f]{64}$")]


class Decision(BaseModel):
    model_config = ConfigDict(extra="forbid", frozen=True, strict=True)

    decision: Literal["approve", "reject"]
    content_version: _ContentVersion


class Approval(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True)

    request_id: str = Field(min_length=1, max_length=256)
    target: str
    action_summary: str
    content_version: _ContentVersion
    expires_at: FiniteFloat
    state: str


class GatewayApproval(Approval):
    run_id: str = Field(pattern=r"^[A-Za-z0-9_-]+$")


class GatewayApprovals(BaseModel):
    approvals: list[GatewayApproval]


class DashboardApproval(Approval):
    agent: str


class DashboardApprovals(BaseModel):
    approvals: list[DashboardApproval]
    biometric: bool


@dataclass(frozen=True, slots=True)
class _LocatedApproval:
    row: GatewayApproval
    agent: str
    base: str
    headers: dict[str, str] = field(repr=False)


class Resolution(BaseModel):
    model_config = ConfigDict(frozen=True, strict=True)

    request_id: str
    run_id: str
    choice: Literal["once", "deny"]
    resolved: int = Field(ge=1, le=1)


def _require_session(request: Request) -> Session:
    session = getattr(request.state, "session", None)
    if session is None:
        raise HTTPException(status_code=401, detail="Unauthorized")
    return session


def _load_config() -> env.Env:
    from hermes_cli.config_backend import ConfigBackendUnavailable

    try:
        return env.load()
    except (ConfigBackendUnavailable, OSError) as exc:
        raise HTTPException(status_code=503, detail="approval_configuration_unavailable") from exc


async def _agent_gateway(agent: str) -> tuple[str, dict[str, str]]:
    try:
        return await run_in_threadpool(lina_talk._headers, agent)
    except (OSError, ValueError) as exc:
        raise HTTPException(status_code=502, detail="approval_gateway_unavailable") from exc


async def _list_approvals(client: httpx.AsyncClient, config: env.Env) -> list[_LocatedApproval]:
    approvals = []
    supported = 0
    for agent in config.agents:
        base, headers = await _agent_gateway(agent)
        try:
            response = await client.get(f"{base}/v1/approvals", headers=headers, timeout=10)
            if response.status_code == 501:
                continue
            response.raise_for_status()
            listing = GatewayApprovals.model_validate_json(response.content)
        except (httpx.HTTPError, ValueError) as exc:
            # An unknown queue could contain a second match for a request ID.
            raise HTTPException(status_code=502, detail="approval_lookup_unavailable") from exc
        supported += 1
        approvals.extend(_LocatedApproval(row, agent, base, headers) for row in listing.approvals)
    if config.agents and not supported:
        raise HTTPException(status_code=501, detail="approvals_unsupported")
    return approvals


@router.get("/api/lina/approvals", dependencies=[Depends(_require_session)])
async def list_approvals() -> DashboardApprovals:
    config = await run_in_threadpool(_load_config)
    async with lina_talk.http_client() as client:
        approvals = await _list_approvals(client, config)
    return DashboardApprovals(
        approvals=[DashboardApproval(agent=entry.agent, **entry.row.model_dump(exclude={"run_id"}))
                   for entry in approvals],
        biometric=config.section("approvals").get("biometric", False) is True,
    )


@router.post("/api/lina/approvals/{request_id}")
async def resolve_approval(
    request_id: str, body: Decision, session: Annotated[Session, Depends(_require_session)],
) -> Resolution:
    config = await run_in_threadpool(_load_config)
    subject = principal_subject({"user_id": session.user_id, "provider": session.provider, "issuer": session.issuer})
    choice = "once" if body.decision == "approve" else "deny"
    async with lina_talk.http_client() as client:
        matches = [entry for entry in await _list_approvals(client, config) if entry.row.request_id == request_id]
        if not matches:
            raise HTTPException(status_code=404, detail="approval_not_pending")
        if len(matches) != 1:
            raise HTTPException(status_code=409, detail="approval_request_ambiguous")
        entry = matches[0]
        if entry.row.state != "pending" or entry.row.content_version != body.content_version:
            raise HTTPException(status_code=409, detail="approval_request_changed")
        run_id = entry.row.run_id
        try:
            response = await client.post(
                f"{entry.base}/v1/runs/{run_id}/approval", headers=entry.headers, timeout=10,
                json={"request_id": request_id, "choice": choice, "content_version": body.content_version,
                      "button_subject": subject, "check_method": "button"},
            )
            if response.status_code in (404, 409, 410, 501):
                raise HTTPException(status_code=response.status_code, detail="approval_gateway_refused")
            response.raise_for_status()
            result = Resolution.model_validate_json(response.content)
        except (httpx.HTTPError, ValueError) as exc:
            # The gateway may already have consumed and recorded the decision.
            raise HTTPException(status_code=502, detail="approval_confirmation_unavailable") from exc
    if (result.request_id, result.run_id, result.choice) != (request_id, run_id, choice):
        raise HTTPException(status_code=502, detail="approval_confirmation_unavailable")
    return result
