"""The shared Desk for clients: where it is, and which file a ``desk:`` reference names."""

from __future__ import annotations

import asyncio

from fastapi import APIRouter, HTTPException

from hermes_cli.desk import DeskError, desk_info, resolve_desk_ref, to_desk_ref

router = APIRouter()


@router.get("/api/desk")
async def get_desk():
    return await asyncio.to_thread(desk_info)


def _resolve(ref: str) -> dict:
    path = resolve_desk_ref(ref)
    return {"ref": to_desk_ref(path), "path": str(path), "exists": path.exists(), "is_dir": path.is_dir()}


@router.get("/api/desk/resolve")
async def resolve_desk(ref: str):
    try:
        return await asyncio.to_thread(_resolve, ref)
    except DeskError as exc:
        raise HTTPException(status_code=400, detail=str(exc)) from exc
