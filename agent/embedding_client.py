"""The one embedding model every memory source is searched with (lina#19).

Agent turns and external messages (``memory.messages``), Honcho's messages and conclusions and the context
summaries are all embedded with one model at one width, so one query vector ranks them together in
``vec.embeddings``. The model is called through an OpenAI-compatible ``POST {base_url}/embeddings``; the
settings are ``embedding:`` in config.yaml and the key is a secret in ``.env`` (docs/store.md):

    embedding:
      base_url: <OpenAI-compatible endpoint, ending in /v1>
      model: qwen3-embedding-8b   # through magpie: its catalog name, e.g. hub/nous/qwen/qwen3-embedding-8b
      dimensions: 1024
      api_key_env: LINA_EMBEDDING_API_KEY

The model string is part of every stored vector's key, so a renamed model is a new set of vectors. magpie names
a model ``<remote>/<provider>/<id>`` as ``GET {base_url}/models`` lists it and refuses the bare id; the first
request turns that refusal into one line naming ``embedding.model``, the endpoint and the closest embedding model
the endpoint lists (``_unknown_model``).

The vectors are Matryoshka prefixes: the first ``dimensions`` values of the model's vector, L2-normalised
again. A server that ignores the ``dimensions`` request field and answers at full width is cut the same way,
and so is a full-width vector stored earlier by the same model (``truncate_stored``).
"""

from __future__ import annotations

import difflib
import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Optional

DEFAULT_MODEL = "qwen3-embedding-8b"
DEFAULT_DIMENSIONS = 1024
DEFAULT_KEY_ENV = "LINA_EMBEDDING_API_KEY"
DEFAULT_BATCH_SIZE = 64
DEFAULT_TIMEOUT = 60.0


class EmbeddingError(RuntimeError):
    """The embedding endpoint could not be used: not configured, unreachable, or an answer that is not vectors."""


class EmbeddingModelMismatch(ValueError):
    """A stored vector came from another model; cutting it would mix two vector spaces in one index."""


def normalize(values: Sequence[float]) -> list[float]:
    norm = math.sqrt(math.fsum(v * v for v in values))
    if norm == 0.0 or not math.isfinite(norm):
        raise ValueError("cannot normalise a zero or non-finite vector")
    return [v / norm for v in values]


def matryoshka(values: Sequence[float], dimensions: int) -> list[float]:
    """The first *dimensions* values, L2-normalised again; a narrower vector is refused, never padded."""
    if len(values) < dimensions:
        raise ValueError(f"vector has {len(values)} values, fewer than the {dimensions} asked for")
    return normalize([float(v) for v in values[:dimensions]])


def truncate_stored(values: Sequence[float], *, stored_model: str, model: str,
                    dimensions: int = DEFAULT_DIMENSIONS) -> list[float]:
    """A vector stored at full width (e.g. 4096 or 1536 values) by *model*, cut to *dimensions* and
    renormalised. Refuses a vector another model wrote: its space does not match, only re-embedding helps."""
    if stored_model != model:
        raise EmbeddingModelMismatch(
            f"the stored vector is from {stored_model!r}, not {model!r}; re-embed it (`lina store reembed`)")
    return matryoshka(values, dimensions)


@dataclass(frozen=True)
class EmbeddingSettings:
    base_url: str
    model: str = DEFAULT_MODEL
    dimensions: int = DEFAULT_DIMENSIONS
    api_key: Optional[str] = None
    batch_size: int = DEFAULT_BATCH_SIZE
    timeout: float = DEFAULT_TIMEOUT
    send_dimensions: bool = True

    @classmethod
    def from_config(cls, config: Optional[Mapping[str, Any]] = None) -> EmbeddingSettings:
        """``embedding:`` from config.yaml (the loaded config when *config* is None); the key from the
        environment or ``.env`` under the name ``api_key_env`` gives."""
        if config is None:
            from hermes_cli.config import load_config
            config = load_config()
        section = dict(config.get("embedding") or {})
        base_url = str(section.get("base_url") or "").strip()
        if not base_url:
            raise EmbeddingError("no embedding endpoint: set embedding.base_url in config.yaml (docs/store.md)")
        from hermes_cli.config import get_env_value
        key_env = str(section.get("api_key_env") or DEFAULT_KEY_ENV)
        return cls(
            base_url=base_url.rstrip("/"),
            model=str(section.get("model") or DEFAULT_MODEL),
            dimensions=int(section.get("dimensions") or DEFAULT_DIMENSIONS),
            api_key=(get_env_value(key_env) or None),
            batch_size=max(1, int(section.get("batch_size") or DEFAULT_BATCH_SIZE)),
            timeout=float(section.get("timeout") or DEFAULT_TIMEOUT),
            send_dimensions=bool(section.get("send_dimensions", True)),
        )


class EmbeddingClient:
    """Embeds text with the configured model, *dimensions* wide and unit length."""

    def __init__(self, settings: EmbeddingSettings, *, transport: Any = None) -> None:
        import httpx
        self.settings = settings
        headers = {"Authorization": f"Bearer {settings.api_key}"} if settings.api_key else {}
        self._http = httpx.Client(base_url=settings.base_url, headers=headers, timeout=settings.timeout,
                                  transport=transport)

    @property
    def model(self) -> str:
        return self.settings.model

    @property
    def dimensions(self) -> int:
        return self.settings.dimensions

    def close(self) -> None:
        self._http.close()

    def __enter__(self) -> EmbeddingClient:
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def embed(self, texts: Sequence[str]) -> list[list[float]]:
        vectors: list[list[float]] = []
        size = self.settings.batch_size
        for start in range(0, len(texts), size):
            vectors.extend(self._embed_batch(list(texts[start:start + size])))
        return vectors

    def embed_one(self, text: str) -> list[float]:
        return self.embed([text])[0]

    def _embed_batch(self, texts: list[str]) -> list[list[float]]:
        import httpx
        body: dict[str, Any] = {"model": self.settings.model, "input": texts, "encoding_format": "float"}
        if self.settings.send_dimensions:
            body["dimensions"] = self.settings.dimensions
        try:
            response = self._http.post("/embeddings", json=body)
        except httpx.HTTPError as exc:
            raise EmbeddingError(f"embedding request to {self.settings.base_url} failed: {exc}") from exc
        if response.status_code == 404 and "knows no model" in response.text:
            raise EmbeddingError(self._unknown_model())
        if response.status_code != 200:
            raise EmbeddingError(f"embedding endpoint answered {response.status_code}: {response.text[:300]}")
        try:
            data = response.json()["data"]
            ordered = sorted(data, key=lambda item: item["index"])
            raw = [item["embedding"] for item in ordered]
        except (ValueError, KeyError, TypeError) as exc:
            raise EmbeddingError(f"embedding endpoint answered without vectors: {response.text[:300]}") from exc
        if len(raw) != len(texts):
            raise EmbeddingError(f"asked for {len(texts)} vectors, the endpoint returned {len(raw)}")
        try:
            return [matryoshka(vector, self.settings.dimensions) for vector in raw]
        except (ValueError, TypeError) as exc:
            raise EmbeddingError(f"embedding endpoint returned an unusable vector: {exc}") from exc

    def _unknown_model(self) -> str:
        """The one line for magpie's "knows no model": the setting, the endpoint and the closest embedding model
        its ``/models`` lists (the same id with a catalog prefix first, else the most similar name)."""
        import httpx
        model, base_url = self.settings.model, self.settings.base_url
        head = f"embedding.model {model!r} is not a model {base_url} knows"
        try:
            listed = [str(item["id"]) for item in self._http.get("/models").json()["data"]]
        except (httpx.HTTPError, ValueError, KeyError, TypeError) as exc:
            return f"{head}; its model list (GET {base_url}/models) could not be read: {exc}"
        embedding = [name for name in listed if "embed" in name.lower()]
        same_id = [name for name in embedding if name.rsplit("/", 1)[-1] == model.rsplit("/", 1)[-1]]
        closest = same_id or difflib.get_close_matches(model, embedding, n=1, cutoff=0.0)
        if not closest:
            return f"{head}; GET {base_url}/models lists no embedding model"
        return (f"{head}; the closest embedding model GET {base_url}/models lists is {closest[0]!r}: "
                f"set embedding.model to it (the name keys every stored vector)")
