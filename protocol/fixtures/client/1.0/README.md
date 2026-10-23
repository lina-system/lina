# Client 1.0 conformance corpus

These synthetic fixtures exercise the [client schema](../../../schema/client/1.0/envelope.json)
and the receive boundaries described in the [host contract](../../../../docs/design/host-protocol.md#conformance-fixtures).
The Go runner is in `kit/protocol/conformance`; run it and both adapters with:

```sh
go test ./kit/protocol/... ./internal/clientapi/...
```

Every JSON file outside `cases/`, `invalid/` and `application/` is a valid envelope. Each must
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
the strict server refuses each with `malformed`; the client discards unknown
payload fields for known kinds and ignores and counts unknown kinds whose common
envelope is otherwise valid. Common envelope fields remain strict. Recovery cases end
with a valid envelope. An unknown kind is repeated to check that counts represent
frames, without claiming application deduplication.

The activity cases cover all turn phases, tool start and finish (both outcomes,
zero and fractional duration), optional stream metadata, and invalid lifecycle
fields followed by a valid event. `conversation/phase_snapshot.json` carries the
retained turn phase. `conversation/activity_snapshot.json` exercises the schema's
tool stream-item shape and the older receiver's unknown-item handling; Core does
not retain tool activities in Topics snapshots. They are live/replay display
events, outside conversation messages and model history.

The running cases round-trip typed state, summary and waiting reason fields in
live updates and retained snapshots alongside the unchanged legacy text.

`TestOlderSchemaClientReceivesPresentationAdditions` in `kit/protocol` generates and
compiles a separate client without the activity fields/kind or typed running
presentation. It retains running text and identity in both live and snapshot
frames while discarding the new fields. With the updated
receive boundary it accepts extended turn states, skips tool activity and unknown
snapshot items, and continues receiving known events. It also verifies that its
strict codec rejects the extended turn state. This proves the updated receiver
works with the earlier schema; it does not claim that an already installed strict
binary supports the additions. Negotiation and subscription behavior are outside
this decoder proof.

The stream cases add `snapshot`, `resync` and `resync_required`, optional
metadata for existing event kinds, subscribe/unsubscribe requests and the
maximum uint64 cursor. Invalid cursors cover noncanonical epochs, negative or
out-of-range sequences and a string where an integer is required. Existing
reserved empty payloads remain in their original cases. The M1 topic scenario
separately establishes replay idempotence and topic-local overflow over Core's
client socket; conformance alone checks the shape and exact values.

The runner checks the adapters' own counters and refusal codes; it does not
compute a passing outcome from expected data. Adapters must use a fresh receiver
for each case. Round-trip comparisons do not pass JSON numbers through float64.

## Application fixtures

`application/` runs through `conformance.LoadApplications` and
`conformance.RunApplication`. Each case establishes fresh service state and
contains `service`, `setup`, `input`, `output` and `outcome`. The runner gives
only `input` to the executing adapter, compares its response to `output`, then
separately reads and compares recorded state to `outcome`. Unknown fields,
missing data and an empty corpus fail loading. JSON values are compared exactly,
including numbers. The corpus inventory rejects cases without a Core consumer.

- `agents/` contains schema-valid input envelopes. The installed Core scenario
  replaces only the synthetic target agent id, supplies live connection
  metadata, and checks response correlation, Core identity and epoch. Expected
  output projects kind, effect state and refusal code; generated message ids and
  timestamps are not fixed. Cases cover conversation start, history, input and
  interrupt for missing/deleted agents, choose/rename/delete for deleted agents,
  and binding or replacing a channel scope with a deleted target. Setup pairs a
  real channel store and optionally binds another active agent first. Recorded
  outcomes compare complete relevant canon rows before and after the request,
  including the existing binding and scope, rather than trusting a refusal alone.
- `results/` uses the existing native `Present`/`Read` service, not a client wire
  payload. All eight body types are stored and read through Core's actual result
  store and ledger. Unknown body fields are refused for each type. Outputs check
  the complete body; outcomes check ledger/head/save counts, revision, preview
  status, provenance and preservation of complete Core-rendered text. Malformed
  bodies must create no result head, event or save.

Run the actual Core adapters with:

```sh
go test ./internal/core/results -run TestCoreResultConformance -count=1
go test ./e2e -run TestM1AgentLifecycleConformance -count=1
```

`e2e/m1/conformance-agents.json` selects the shared agent corpus for the headless
harness; it starts installed Core and uses its public socket. The native result
cases establish service behavior only, not client wire delivery or rendering.

## Remaining

Client result body, saved reference and destination schemas remain reserved;
`hello` capabilities have no result-type list and Core has no client result
read/edit/save handlers. Full wire bodies and result-type negotiation (including
an undeclared type and a client without `results`), stale edit/save, double-save
and erased-conversation wire fixtures depend on
[#419](https://github.com/thisisjun786/lina/issues/419). Native body fixtures and
card-only wire fixtures do not establish these behaviors.

Older/newer peer declarations round-trip here; selection/refusal is exercised
separately by the supported-combination and client-admission scenarios. Normal
application flow for other reserved kinds and recording their effect outcomes
still require their services; receive-boundary cases alone do not execute them.

Node's stale-epoch, reboot and lost-receipt fixtures belong to the Node protocol
version, outside this client 1.0 corpus.

The surface fixtures carry protocol 1.1. They cover the lina.cards catalog,
choice values, whole bodies, and refusal of surface/action frames in 1.0.
The corpus directory retains its original name so existing consumers keep
loading the same conformance suite.
