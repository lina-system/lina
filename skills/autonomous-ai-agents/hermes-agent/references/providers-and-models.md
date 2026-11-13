# magpie and Models

LINA model calls use the magpie provider, registered by `plugins/model-providers/magpie/__init__.py`. magpie routes each `<provider>/<model>` identifier to its upstream; do not copy a vendor-provider table from another distribution.

1. Use `lina model` or `lina setup` to inspect selection. Keep the selected provider as `magpie` and choose a model magpie actually exposes.
2. Inspect `model.provider`, `model.default`, and `model.base_url` with `lina config get <key>`. Use the magpie endpoint configured for this installation, not an inferred vendor endpoint.
3. Keep `MAGPIE_API_KEY` secret. The provider also recognizes `MAGPIE_BASE_URL`; preserve the installation's routing configuration.
4. Verify with `lina chat --oneshot -q "Reply with ready."`.

magpie speaks OpenAI-compatible chat completions and model discovery. The provider supplies a `session-id` header for conversation affinity and translates supported reasoning effort. Preserve this header path when extending transport code.

Auxiliary tasks and delegated calls also need valid magpie routing. Diagnose their resolved model/provider rather than adding unrelated vendor keys. A plugin's service credential is separate from model inference authentication; an external application does not automatically inherit LINA's credentials. Do not copy auth-store tokens to another application.
