"""#752: contextual validation and recoverable agent provenance before writing.

These tests inject model, embedding and database I/O boundaries. Derivation,
representation saving, document conversion and API serialization remain real.
They run without application conftest fixtures or external services.
"""

import json
from collections.abc import AsyncIterator, Callable
from contextlib import asynccontextmanager
from datetime import UTC, datetime
from pathlib import Path
from unittest.mock import AsyncMock

import pytest
from pydantic import BaseModel
from sqlalchemy.ext.asyncio import AsyncSession

from src import crud, models, schemas
from src.crud.document import _document_model_from_create
from src.deriver import deriver
from src.exceptions import EmptyRepresentationError
from src.llm import HonchoLLMCallResponse
from src.utils.config_helpers import get_configuration
from src.utils.representation import PromptRepresentation


@pytest.fixture(autouse=True)
def offline(monkeypatch: pytest.MonkeyPatch) -> None:
    """Fail immediately on unexpected network I/O."""
    import socket

    def deny_network(*_args, **_kwargs) -> None:
        raise AssertionError("Synthetic conclusion tests must not use the network")

    monkeypatch.setattr(socket.socket, "connect", deny_network)
    monkeypatch.setattr(deriver, "emit", lambda *_args: None)


async def _write_batch(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    *,
    source: str,
    context: str,
    candidates: list[str],
    supported_indices: list[int],
    source_indices: list[int],
    speaker: str = "owner",
    clipped_verification: bool = False,
) -> tuple[list[schemas.Conclusion], list[list[str]], list[str]]:
    now = datetime(2026, 1, 1, tzinfo=UTC)
    messages = [
        models.Message(
            id=1,
            public_id="source_message_000001",
            workspace_name="synthetic-workspace",
            session_name="synthetic-session",
            peer_name="synthetic-peer",
            content=source,
            h_metadata={"lina_speaker": speaker, "lina_grade": speaker},
            seq_in_session=1,
            token_count=10,
            created_at=now,
        )
    ]
    if context:
        messages.insert(
            0,
            models.Message(
                id=2,
                public_id="context_message_00001",
                workspace_name="synthetic-workspace",
                session_name="synthetic-session",
                peer_name="other-peer",
                content=context,
                h_metadata={"lina_speaker": "owner", "lina_grade": "owner"},
                seq_in_session=0,
                token_count=10,
                created_at=now,
            ),
        )
    generated = PromptRepresentation.model_validate(
        {
            "explicit": [
                {"content": text, "source_indices": source_indices}
                for text in candidates
            ]
        }
    )
    model_calls: list[str] = []

    async def model_response(
        *,
        response_model: type[BaseModel],
        prompt: str,
        validate_response: Callable[[HonchoLLMCallResponse[BaseModel]], None]
        | None = None,
        **_kwargs,
    ) -> HonchoLLMCallResponse[BaseModel]:
        model_calls.append(prompt)
        is_generation = response_model is PromptRepresentation
        payload = (
            generated
            if is_generation
            else response_model.model_validate({"supported_indices": supported_indices})
        )
        result = HonchoLLMCallResponse(
            content=payload,
            input_tokens=100,
            output_tokens=10,
            finish_reasons=["STOP"],
        )
        if validate_response is not None:
            validate_response(result)
        result.hit_input_token_cap = clipped_verification and not is_generation
        return result

    @asynccontextmanager
    async def transaction(_name: str) -> AsyncIterator[AsyncSession]:
        yield AsyncMock(spec=AsyncSession)

    written: list[schemas.Conclusion] = []

    async def store_documents(
        _db: AsyncSession,
        documents: list[schemas.DocumentCreate],
        workspace_name: str,
        *,
        observer: str,
        observed: str,
        **_kwargs,
    ) -> crud.CreateDocumentsResult:
        for document in documents:
            row = _document_model_from_create(
                document,
                workspace_name=workspace_name,
                observer=observer,
                observed=observed,
            )
            row.id = "conclusion_row_0000001"
            row.created_at = now
            written.append(schemas.Conclusion.model_validate(row))
        return crud.CreateDocumentsResult()

    embedded: list[list[str]] = []

    async def embed(texts: list[str], **_kwargs) -> list[list[float]]:
        embedded.append(texts)
        return [[0.1] * 3 for _ in texts]

    monkeypatch.setattr(deriver, "honcho_llm_call", model_response)
    monkeypatch.setattr("src.crud.representation.tracked_db", transaction)
    monkeypatch.setattr(crud, "get_or_create_collection", AsyncMock())
    monkeypatch.setattr(crud, "create_documents", store_documents)
    monkeypatch.setattr(
        "src.crud.representation.embedding_client.simple_batch_embed", embed
    )
    configuration = get_configuration(None, None, None)
    configuration.dream.enabled = False
    try:
        await deriver.process_representation_tasks_batch(
            messages,
            configuration,
            observers=["synthetic-peer"],
            observed="synthetic-peer",
            queue_item_message_ids=[1],
            session_id="synthetic-session-id",
        )
    finally:
        capture = {
            "source": source,
            "context": context,
            "source_metadata": messages[-1].h_metadata,
            "candidates": candidates,
            "supported_indices": supported_indices,
            "model_calls": len(model_calls),
            "embedded": embedded,
            "written": [row.model_dump(mode="json", by_alias=True) for row in written],
        }
        (tmp_path / "capture.json").write_text(
            json.dumps(capture, ensure_ascii=False, indent=2), encoding="utf-8"
        )
    return written, embedded, model_calls


@pytest.mark.asyncio
@pytest.mark.parametrize("source_indices", [[], [99, -1]])
async def test_agent_origin_survives_missing_or_invalid_citations(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path, source_indices: list[int]
) -> None:
    written, _, _ = await _write_batch(
        monkeypatch,
        tmp_path,
        source="I previously said the server moved on Tuesday.",
        context="What did you say before?",
        candidates=["synthetic-peer said the server moved on Tuesday."],
        supported_indices=[0],
        source_indices=source_indices,
        speaker="agent",
    )
    # Missing citations retain the complete batch, including the agent's ID.
    assert [row.source_message_ids for row in written] == [
        ["context_message_00001", "source_message_000001"]
    ]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("context", "source", "candidate", "supported_indices", "expected"),
    [
        (
            "서버와 네트워크 연결을 이전했어요.",
            "허브를 옮기고 케이블을 다시 연결했어요.",
            "synthetic-peer moved herb plants.",
            [],
            [],
        ),
        (
            "서버와 네트워크 연결을 이전했어요.",
            "허브를 옮기고 케이블을 다시 연결했어요.",
            "synthetic-peer moved the server hub and reconnected its cables.",
            [0],
            ["synthetic-peer moved the server hub and reconnected its cables."],
        ),
        (
            "정원의 화분에 물을 주었어요.",
            "허브를 햇볕이 드는 화분으로 옮겼어요.",
            "synthetic-peer moved herb plants to a sunny pot.",
            [0],
            ["synthetic-peer moved herb plants to a sunny pot."],
        ),
        (
            "서버와 네트워크 연결을 이전했어요.",
            "허브를 옮기고 케이블을 다시 연결했어요.",
            "synthetic-peer moved herb plants.",
            [99, -1],
            [],
        ),
    ],
)
async def test_only_contextually_supported_conclusions_reach_storage(
    monkeypatch: pytest.MonkeyPatch,
    tmp_path: Path,
    context: str,
    source: str,
    candidate: str,
    supported_indices: list[int],
    expected: list[str],
) -> None:
    written, embedded, model_calls = await _write_batch(
        monkeypatch,
        tmp_path,
        source=source,
        context=context,
        candidates=[candidate],
        supported_indices=supported_indices,
        source_indices=[1, 0, 1],
    )
    assert [row.content for row in written] == expected
    assert embedded == ([expected] if expected else [])
    # The actual verifier request carries the full surrounding source language.
    assert len(model_calls) == 2
    assert source in model_calls[1] and context in model_calls[1]
    if written:
        assert written[0].source_message_ids == [
            "source_message_000001",
            "context_message_00001",
        ]


@pytest.mark.asyncio
async def test_verifier_selects_original_candidates_without_reordering(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    candidates = [
        "synthetic-peer moved the server hub.",
        "synthetic-peer moved herb plants.",
        "synthetic-peer reconnected the network cables.",
    ]
    written, embedded, _ = await _write_batch(
        monkeypatch,
        tmp_path,
        source="허브를 옮기고 케이블을 다시 연결했어요.",
        context="서버와 네트워크 연결을 이전했어요.",
        candidates=candidates,
        supported_indices=[2, 0, 2, 99],
        source_indices=[1],
    )
    assert [row.content for row in written] == [candidates[0], candidates[2]]
    assert embedded == [[candidates[0], candidates[2]]]


@pytest.mark.asyncio
async def test_verification_with_clipped_context_cannot_approve_storage(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    with pytest.raises(EmptyRepresentationError):
        await _write_batch(
            monkeypatch,
            tmp_path,
            source="허브를 옮겼어요.",
            context="서버와 네트워크 연결을 이전했어요.",
            candidates=["synthetic-peer moved herb plants."],
            supported_indices=[0],
            source_indices=[1],
            clipped_verification=True,
        )
    capture = json.loads((tmp_path / "capture.json").read_text(encoding="utf-8"))
    assert capture["written"] == []
    assert capture["embedded"] == []


@pytest.mark.asyncio
async def test_empty_generation_does_not_call_verifier(
    monkeypatch: pytest.MonkeyPatch, tmp_path: Path
) -> None:
    written, embedded, model_calls = await _write_batch(
        monkeypatch,
        tmp_path,
        source="Thanks.",
        context="",
        candidates=[],
        supported_indices=[],
        source_indices=[],
    )
    assert written == []
    assert embedded == []
    assert len(model_calls) == 1
