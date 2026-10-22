# Linux non-competition evidence

This directory implements the record check for tiers 1 and 2 of the
[non-competition contract](../../docs/design/non-competition.md#evidence-ladder).
The [Linux measurement set](records/linux.json) is empty: no human concurrent
Linux session has been submitted. Both tiers and all eight columns remain
unverified. An empty set is valid bookkeeping, never passing evidence.

The [first supporting run](headless-protocol.json) records the command and
source hashes for the headless scenario in
[e2e/m3/non-competition.json](../../e2e/m3/non-competition.json). The scenario
starts installed Core, creates a real versioned record through its model tool
loop, refuses a filesystem path supplied as a record id, and preserves a newer
external edit when a subsequent tool call uses the old revision. It checks
receipts, saved bytes and the unchanged Git head after refusal. The model
responses and the person's edit are scripted. It does not measure GUI events,
clipboard, resource yield, a dedicated browser, session lifecycle, the full
background load, or human concurrent activity. It cannot pass a ladder cell.

## Recording a session

1. Select one Linux installation. Use an opaque installation id, and stable
   aliases for the observer, OS user, GUI session, profiles, task and grant.
   Keep the identity mapping private. Do not put hostnames, real user names,
   conversations, credentials, personal files or raw operational logs here.
2. Copy [record-template.json](record-template.json) into the `records` array
   for each column of tier 1. Fill every null from a real observation; null is
   allowed only for the explicitly absent LINA GUI session, profile and seat
   lease. The template is intentionally invalid until filled.
3. Before the run, declare input latency, yield and recovery limits, and CPU
   and resident memory limits for every process. Preserve this declaration
   with the observation. The resident budgets must include the install's
   [packaging and resident budget](../../docs/design/runtime.md#packaging-and-resident-budget).
   Do not raise a bound to make a completed run pass.
4. Run the person's work and LINA's granted work concurrently, with every
   normally running LINA process and LIFE activity present. Inventory process
   instances (including workers, tools, plugins and external components),
   total agents, agents doing background work, and background runs. Declare
   inactive optional components explicitly; don't drop active processes.
5. Collect input, focus, clipboard and file access observations for the whole
   session, correlated with task and grant ids. Exercise stale writes,
   lock/unlock, sleep/resume, observed disconnect and silent expiry. Do not
   infer zero events from a missing or disabled observer. Use synthetic work
   content and sanitized trace exports; preserve timing, ids and event counts
   needed to audit each criterion. Hash the exact exported attachment bytes.
6. Store one complete record per applicable column. All eight columns apply
   to each shared Linux row; CLI execution does not exempt the focus, input,
   clipboard or lifecycle columns. Records may share attachments when the
   traces cover each entire session. Missing cells remain unverified. An
   observed failure is recorded as `fail`, never discarded or marked absent.
7. Check tier 1. Only when its eight cells pass may tier 2 begin. For tier 2,
   use a dedicated LINA browser profile, identified in the grant and distinct
   from every human profile. Finish all tier 1 observations before starting
   tier 2. Stop on a failure. Keep a failed attempt in its own measurement set;
   a later attempt is a new set, not an overwritten failure.

## Format and checks

Each measurement set is `{ "schema_version": 1, "records": [...] }` for one
installation, host OS version and install mode. Its record ids and `(tier,
column)` pairs are unique. Supported modes are `device-os` and `desktop` on
`linux`; VM rows are not passed measurements. Tiers 1 and 2 use no human GUI
session and no seat lease; tier 1 also has no browser profile. These absent
values are explicit JSON nulls.

The template defines the fields consumed by the checker:

| Field | Meaning |
| --- | --- |
| `id`, `tier`, `column` | Record identity and cell. Columns: `focus`, `input`, `clipboard`, `file_scope`, `resources`, `lock_unlock`, `sleep_resume`, `disconnect`. |
| `host`, `session`, `hardware` | Installation alias, Linux version and mode; user/session/profile aliases and task/grant/lease; CPU, physical memory in bytes, relevant devices. |
| `artifacts` | Version and release manifest SHA-256 for every involved artifact. `core`, `node`, `app`, `os`, `claude_adapter` are explicit; additional artifacts such as `moirai_worker` use the same shape. An absent optional artifact has only `absent_reason`. Device-OS requires Core and OS; desktop requires Node and app. Claude-account sessions require the adapter. |
| `workload` | Human, LINA and LIFE work descriptions, agent and background-run counts, whether Claude served the session, process inventory and normal-load process ids. The three true declarations attest human work, LINA work and full background load. |
| `observation` | `manual_linux`, an observer alias, and UTC start/end timestamps. A headless scenario is not this observation method. |
| `traces` | Four relative attachment paths, SHA-256 digests, task/grant ids and whole-session start/end coverage. Paths cannot escape the set directory, including through symlinks. |
| `bounds` | UTC declaration time strictly before the session, latency/yield/recovery in milliseconds, and CPU percent/resident bytes per process. CPU percent uses 100 per logical CPU. |
| `measured` | Nonnegative counts and measurements; resource samples report maxima, not averages. Boolean lifecycle outcomes include whether the event was exercised. Process ids exactly match the declared inventory. |
| `result` | `pass` or `fail`, agreeing with measured criteria. |

Every record reports all seven zero-violation counters. Any focus steal,
injected input, clipboard read/write, out-of-scope access, lost edit or killed
human process fails it. The resources cell additionally compares latency,
yield, recovery and each process's CPU and memory against the bounds. File
scope and lifecycle cells require their exercised/blocked outcomes to be true.
The checker computes measured-minus-bound deltas and per-criterion booleans in
its JSON output; positive deltas fail the resources cell.

```sh
node --test scripts/non-competition.test.mjs
node scripts/non-competition.mjs evidence/non-competition/records/linux.json
go test -count=1 -v ./e2e/m1 -run '^TestM3NonCompetitionInstalledCore$'
```

The Go scenario uses the shared harness in `e2e/m1`; its M3 fixture remains in
`e2e/m3/non-competition.json`.

The CLI reads and hashes every attachment (regular files of at most 32 MiB).
Exit 0 requires **both** tiers complete and passing. Exit 1 includes malformed
input, missing attachments, missing cells, failed cells and a stopped ladder.
For a tier-1-only set, inspect `tiers[0].status` for permission to proceed; exit
1 still means the two-tier acceptance is unfinished. The JSON separates
`valid` (well-formed, consistent evidence) from `passed` (both tiers passed).
An honest failed tier is valid evidence. An attempted tier 2 after a failed or
incomplete tier 1 is invalid and remains unverified.

Foundation discovers the Node tests automatically. They test adversarial
synthetic records and validate the structure and attachments of every committed
set directly under `records/`. They accept an honestly incomplete or failed
set, so green CI is not a claim that the manual ladder passed. The checker
cannot authenticate an observer, prove an inventory complete, or derive event
counts from platform-specific trace formats; those require examination of the
attached evidence by the person accepting the installation.
