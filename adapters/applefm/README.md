# Apple Foundation Models helper

This helper is the Mac side of the on-device provider. Core starts it with one
JSON line on standard input and reads one JSON line on standard output.

Build it on the desk Mac, after the owner accepts the framework license:

```sh
swiftc -parse-as-library -o lina-applefm main.swift
```

Point Core at that binary with `LINA_APPLEFM_HELPER`. The default model is
`apple-foundation-on-device`. `LINA_APPLEFM_PRIVATE_CLOUD=1` selects
`apple-foundation-private-cloud`. The public framework has no third-party
Private Cloud Compute session, so the helper reports that model unavailable
and Core falls back to the person's main model.

`profile` returns the context size, tool calling, guided generation, and image
input. `complete` uses guided generation when a schema is present. A refusal,
a context overflow, or a value that does not match the schema is a schema miss
with no object. The helper does not explain the miss or fill a field.
