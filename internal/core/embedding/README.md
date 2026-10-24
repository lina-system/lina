# Embedding adapter

The non-chat adapter sends masked, bounded inputs to the person's configured
OpenAI-compatible embeddings URL. It uses the `embedding` secret-store service,
refuses redirects, bounds responses and validates vector count, indices, model,
dimension and finite nonzero values. Provider response bodies and URLs never
appear in errors. There is no automatic HTTP retry.

Stop Core before configuring it:

```sh
lina embedding --state-root <state> --endpoint https://example.com/v1/embeddings --person <person-id> --key-name endpoint --key-stdin
```

Supply the key on standard input. The key enters the existing secret store;
`system/embedding.json` contains only endpoint, model, dimension and key name.
An endpoint without authentication can omit the key options. HTTP is accepted
only for loopback endpoints. Restart Core and run the installed `moirai-worker`
to fill the durable queue. Use `--model` and `--dimension` for another model;
`lina embedding --state-root <state> --disable` restores full-text-only reads.

Configuring an endpoint consents to embedding LINA's conversations. Import
commands require a separate `--embed` opt-in and print the import's volume.
They never import source vectors. Never-leaves evidence, unconsented imports,
disabled messenger chats and secret spans do not reach the endpoint.

Default: `BAAI/bge-m3`, 1,024 dimensions. This retains the measured model from
#1040. The adapter's character bound differs from that study's tokenizer bound;
the private #849 model/quality comparison is still outstanding. Fake endpoint
tests prove transport, masking and lifecycle behavior, not semantic quality.
