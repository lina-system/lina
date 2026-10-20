# Client 1.0 conformance corpus

These synthetic fixtures exercise the [client schema](../../../schema/client/1.0/envelope.json)
and the receive boundaries described in the [host contract](../../../../docs/design/host-protocol.md#conformance-fixtures).
The Go runner is in `kit/protocol/conformance`; run it and both adapters with:

```sh
go test ./kit/protocol/... ./internal/clientapi/...
```

Every JSON file outside `cases/` and `invalid/` is a valid envelope. Each must
round-trip through an implementation's codec without changing JSON values.
Whitespace and object key order are irrelevant; exact numbers are retained.
`normal/` supplies every kind not already represented by the root handshake
files. `outcomes/`, `results/`, `values/` and `versions/` add current-schema
variants. An empty reserved payload tests only the declared wire shape.

The eight result-type fixtures in `results/` are unprompted cards with
`reply_to: null`; their payloads omit both `body` and `text`, as required for
creation, revision and status notifications. `cases/normal_client.json` uses
these same card files for its input and expected admitted output. They do not
represent answers to `result_get`.

Each `cases/*.json` describes a fresh receive boundary:

| Field | Meaning |
| --- | --- |
| `side` | `server` or `client`, the receiving implementation |
| `input` | Ordered paths of envelopes to receive, relative to this directory |
| `output` | Ordered paths of admitted envelopes after decode and encode |
| `errors` | One typed error code per input; empty string means no refusal |
| `outcome` | The receiver's own `accepted`, `malformed` and `unknown_kinds` counters |

`output` is decoder output, not an application reply. No rejected or ignored
input may produce an admitted envelope. Every envelope is used by a case.
`invalid/` contains syntactically valid JSON that fails the canonical schema:
the strict server refuses each with `malformed`; the client ignores and counts
only unknown kinds whose common envelope is otherwise valid. Recovery cases end
with a valid envelope. An unknown kind is repeated to check that counts represent
frames, without claiming application deduplication.

The runner checks the adapters' own counters and refusal codes; it does not
compute a passing outcome from expected data. Adapters must use a fresh receiver
for each case. Round-trip comparisons do not pass JSON numbers through float64.

## Remaining

The following application scenarios require the deferred schemas or services;
none is claimed as passing by a reserved empty payload or a card-only fixture:

- Conversation requests for a nonexistent agent and for a deleted agent need
  agent/conversation state. Agent management on a deleted agent, channel binding
  to a deleted agent, and changing a channel scope to a deleted agent also need
  payload fields that are currently reserved.
- Full bodies for summary, table, diagram, chart, document draft, comparison,
  plan view and file results need their body schemas and validation. The current
  fixtures cover each type's card fields only.
- A result type the client did not declare needs result-type negotiation. A
  client without `results` needs the ordinary output payload and fallback logic.
- Edit at a stale revision, save at a stale revision, a save sent twice, and a
  result whose conversation was erased need result state and effect handling.
- Older/newer peer declarations round-trip here; selection or refusal needs the
  supported-combination table and handshake service.
- Normal application flow for reserved kinds and recording effect outcomes need
  their services. This corpus preserves effect states but does not execute them.
- The `e2e/m1/` scenario requires the headless harness and runnable Core/TUI.
  Receive-boundary tests do not substitute for it.

Node's stale-epoch, reboot and lost-receipt fixtures belong to the Node protocol
version, outside this client 1.0 corpus.
