"""Degraded verification must not acknowledge a representation queue batch."""

import json
import socket
from datetime import UTC, datetime
from pathlib import Path
from typing import Any
from unittest.mock import AsyncMock

import pytest
from tenacity import wait_none

from src import models
from src.config import ModelConfig, ResolvedFallbackConfig
from src.deriver import deriver, queue_manager
from src.deriver.conclusions import ConclusionVerification
from src.exceptions import EmptyRepresentationError
from src.llm import HonchoLLMCallResponse
from src.llm import api as llm_api
from src.llm.structured_output import StructuredOutputError, validate_structured_output
from src.utils.config_helpers import get_configuration
from src.utils.representation import PromptRepresentation


@pytest.fixture
def batch(monkeypatch: pytest.MonkeyPatch) -> queue_manager.QueueBatchResult:
    def deny_network(*_args: Any, **_kwargs: Any) -> None:
        raise AssertionError("Synthetic verification tests must not use the network")

    monkeypatch.setattr(socket.socket, "connect", deny_network)
    monkeypatch.setattr(deriver, "emit", lambda *_args: None)
    monkeypatch.setattr(llm_api, "log_reasoning_trace", lambda **_kwargs: None)
    monkeypatch.setattr(llm_api, "wait_exponential", lambda **_kwargs: wait_none())
    monkeypatch.setattr(queue_manager, "RETRY_BACKOFF_SECONDS", 0.0)
    config = ModelConfig(
        model="primary-model",
        transport="anthropic",
        api_key="test-key",
        fallback=ResolvedFallbackConfig(
            model="fallback-model", transport="openai", api_key="test-key"
        ),
    )
    monkeypatch.setattr(deriver, "_get_deriver_model_config", lambda: config)
    configuration = get_configuration(None, None, None)
    configuration.dream.enabled = False
    message = models.Message(
        id=1,
        public_id="synthetic_message_001",
        workspace_name="synthetic-workspace",
        session_name="synthetic-session",
        peer_name="synthetic-peer",
        content="The server moved on Tuesday.",
        seq_in_session=1,
        token_count=8,
        created_at=datetime(2026, 1, 1, tzinfo=UTC),
    )
    item = models.QueueItem(
        id=1,
        workspace_name=message.workspace_name,
        session_id="synthetic-session-id",
        message_id=message.id,
        task_type="representation",
        work_unit_key="representation:synthetic-workspace:synthetic-session:synthetic-peer",
        payload={"observers": ["synthetic-peer"]},
        processed=False,
    )
    return queue_manager.QueueBatchResult(
        messages_context=[message], items_to_process=[item], configuration=configuration
    )


async def _process(
    monkeypatch: pytest.MonkeyPatch,
    batch: queue_manager.QueueBatchResult,
    verification: HonchoLLMCallResponse[Any],
    tmp_path: Path,
) -> tuple[list[Exception], list[str], models.QueueItem, int]:
    attempts: list[str] = []

    async def provider(
        _provider: str, model: str, *_args: Any, response_model: Any, **_kwargs: Any
    ) -> HonchoLLMCallResponse[Any]:
        if response_model is PromptRepresentation:
            return HonchoLLMCallResponse(
                content=PromptRepresentation.model_validate(
                    {
                        "explicit": [
                            {"content": "The server moved.", "source_indices": [0]}
                        ]
                    }
                ),
                output_tokens=10,
                finish_reasons=["stop"],
            )
        attempts.append(model)
        if isinstance(verification.content, Exception):
            raise verification.content
        if isinstance(verification.content, dict):
            validate_structured_output(verification.content, ConclusionVerification)
        return verification.model_copy()

    monkeypatch.setattr(llm_api, "honcho_llm_call_inner", provider)
    save = AsyncMock()
    monkeypatch.setattr(deriver.RepresentationManager, "save_representation", save)
    manager = queue_manager.QueueManager()
    item = batch.items_to_process[0]
    work_key = item.work_unit_key
    manager.track_worker_work_unit("synthetic-worker", work_key, "synthetic-claim")
    monkeypatch.setattr(
        manager,
        "get_queue_item_batch",
        AsyncMock(side_effect=[batch, queue_manager.QueueBatchResult()]),
    )
    monkeypatch.setattr(manager, "_cleanup_work_unit", AsyncMock(return_value=False))
    monkeypatch.setattr(
        manager, "_get_work_unit_retry_attempts", AsyncMock(return_value=0)
    )
    retry = AsyncMock()
    monkeypatch.setattr(manager, "_set_work_unit_retry_attempts", retry)
    monkeypatch.setattr(manager, "_clear_work_unit_retry_attempts", AsyncMock())

    async def acknowledge(items: list[models.QueueItem], _key: str) -> None:
        for row in items:
            row.processed = True

    async def mark_error(row: models.QueueItem, _key: str, error: str) -> None:
        row.processed = True
        row.error = error

    monkeypatch.setattr(manager, "mark_queue_items_as_processed", acknowledge)
    monkeypatch.setattr(manager, "mark_queue_item_as_errored", mark_error)
    errors: list[Exception] = []
    handle_error = manager._handle_processing_error

    async def observe_error(error: Exception, *args: Any) -> bool:
        errors.append(error)
        return await handle_error(error, *args)

    monkeypatch.setattr(manager, "_handle_processing_error", observe_error)
    await manager.process_work_unit(work_key, "synthetic-worker")
    capture = {
        "verification_attempts": attempts,
        "errors": [type(error).__name__ for error in errors],
        "processed": item.processed,
        "queue_error": item.error,
        "save_calls": save.await_count,
        "retry_count": retry.call_args.args[1] if retry.called else 0,
    }
    (tmp_path / "capture.json").write_text(
        json.dumps(capture, indent=2), encoding="utf-8"
    )
    assert save.await_count == 0
    return errors, attempts, item, capture["retry_count"]


@pytest.mark.asyncio
@pytest.mark.parametrize(
    ("content", "finish_reason", "input_capped"),
    [
        (ConclusionVerification(supported_indices=[]), "length", False),
        (ConclusionVerification(supported_indices=[0]), "max_tokens", False),
        (ConclusionVerification(supported_indices=[]), "content_filter", False),
        ("not valid JSON", "stop", False),
        ("", "stop", False),
        (None, "stop", False),
        (ConclusionVerification(), "stop", False),
        (ConclusionVerification(supported_indices=[0]), "stop", True),
        ({"supported_indices": "not-a-list"}, "stop", False),
        (StructuredOutputError("Synthetic missing structured response"), "stop", False),
    ],
    ids=[
        "truncated",
        "partial-approval",
        "filtered",
        "malformed",
        "empty",
        "absent",
        "missing-approval",
        "input-capped",
        "parse-error",
        "structured-error",
    ],
)
async def test_degraded_verification_preserves_queue_for_retry(
    monkeypatch: pytest.MonkeyPatch,
    batch: queue_manager.QueueBatchResult,
    tmp_path: Path,
    content: Any,
    finish_reason: str,
    input_capped: bool,
) -> None:
    errors, attempts, item, retries = await _process(
        monkeypatch,
        batch,
        HonchoLLMCallResponse(
            content=content,
            output_tokens=0,
            finish_reasons=[finish_reason],
            hit_input_token_cap=input_capped,
        ),
        tmp_path,
    )
    assert len(errors) == 1 and isinstance(errors[0], EmptyRepresentationError)
    assert not item.processed
    assert item.error is None
    assert retries == 1
    assert attempts == ["primary-model", "primary-model", "fallback-model"]


@pytest.mark.asyncio
async def test_explicit_empty_approval_completes_without_writing(
    monkeypatch: pytest.MonkeyPatch,
    batch: queue_manager.QueueBatchResult,
    tmp_path: Path,
) -> None:
    errors, attempts, item, retries = await _process(
        monkeypatch,
        batch,
        HonchoLLMCallResponse(
            content=ConclusionVerification(supported_indices=[]),
            output_tokens=0,
            finish_reasons=["stop"],
        ),
        tmp_path,
    )
    assert errors == []
    assert attempts == ["primary-model"]
    assert item.processed
    assert item.error is None
    assert retries == 0
