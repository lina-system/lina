# LINA 1 acceptance records

No owner acceptance run is recorded here. The [schema](record.schema.json) and
[runbook](runbook.md) prepare that run; passing the script tests does not accept
LINA 1 or close #123.

`node scripts/accept-m1.mjs <record.json>` checks one record and its attachments.
Exit 0 means every required observation is present and consistent, the last
attempt passes, and the referenced machine checks pass. Exit 1 means invalid,
failed or incomplete evidence. It never installs, signs in, changes state roots
or invents an observation. `--run-headless <directory>` runs the existing Core
and TUI tests and writes a content-addressed receipt, including on test failure.

## Record format 1

The schema is JSON Schema 2020-12. All object fields are required and unknown
fields are rejected. The checker also enforces cross-field rules that JSON
Schema cannot express. The repository's Node test discovery includes
`scripts/accept-m1.test.mjs` in foundation.

| Field in each attempt | Evidence required |
| --- | --- |
| `run_id`, `commit`, `host`, `manifest_sha256` | A new opaque 128-bit run id, full dev commit, supported host code and hash of the actual build manifest. Manifest commit and target must agree; each commit must be reachable from local `origin/dev`. |
| `clean_install` | Built artifacts installed into a new, private state root. |
| `headless_sha256` | The script's receipt for uncached M1 e2e and TUI UI tests on that commit. Every required scenario, all 13 startup boundaries and all seven turn boundaries must pass. The worker outcome scenario includes actual Core tool calls, a worker result file, read-back and restart recovery. |
| `matrix` | Exactly `fresh`, `planned_restart`, `crash_restart`, `restore`, in that order. Each row has a result, READY observation, decimal uint64 epoch, opaque root id, replayed-effect count and owner-session evidence hash. Epochs rise strictly, including above JavaScript's safe integer range. The first three roots agree; restore uses another root. |
| `restore` | The original `generation-` id emitted by backup (RFC 4648 base32 suffix), and zero forgotten hits in search, summaries and ContextPacket after restoring an older generation. |
| `checks` | A1–A9, in the order of the roadmap's LINA 1 acceptance list. Each has `pass`, `fail` or `not_run` and an evidence hash (null when unrun). Passing behavior checks reference this attempt's headless receipt. |
| `failures` | Separate results for startup/turn SIGKILL, corrupt canon, canon write fault, disk full, unusable sandbox, slow client, dropped frames, channel replay, unavailable provider, expired token, usage cap, background SIGKILL and older-generation restore. |
| `ui_ux` | Owner verdict on layout, readability, physical input and interaction, with the owner-session receipt. |
| `terminals` | Confirmed opaque terminal ids and one S1–S11 result per terminal. Each hash identifies the original spike JSON. Its revision and result must match; the existing spike runner rechecks its bounds and measurements. S8 remains explicitly headless. |
| `diagnosis` | Model, interrupted and storage injection results; each passing result includes the independently observed injection root, task query filter, twelve-field events and a structured citation. The checker follows every parent to the root and checks the cited class and cause. |

One record contains an ordered `attempts` array. Keep failed attempts. A fixing
commit requires a new run id, new install roots, a new headless receipt and a
complete new set of checks. Never copy passing rows from the previous commit.
Unrun matrix cells have null observations; unrun diagnoses have null root,
query and citation and an empty event array. An unrun restore is null. These
values preserve an interrupted attempt but cannot pass final acceptance.
Terminal `gap` results retain their original measured files in failed attempts;
they cannot pass the final attempt.

## Attachments and limits

Store each attachment beside the record as `<sha256>.json`; the digest covers
the exact file bytes. Absolute paths, traversal and symlinks outside that
directory are not accepted as attachment references. Duplicate JSON keys are
rejected. Keep raw terminal output, credentials, personal conversations and
state roots out of the repository.

An owner-session receipt is a JSON object with `format: 1`,
`kind: "owner-observation"`, and the attempt's `commit` and `run_id`. It identifies
the hand session; its observations are the typed counts, codes and ids in the
record. It is an owner attestation, not a second automated observation. Build
manifests and original spike records retain their existing formats. The
headless receipt stores only required test ids and result codes, never Go test
output or synthetic conversation text.

The checker verifies recorded facts and attached machine results. It cannot
prove that an owner actually looked at the UI, that an unreported attempt never
failed, or that supplied receipts were honestly collected. Review the evidence
before adding a real record. A green synthetic suite does not establish live
issuer access, a real Telegram session or physical terminal quality.

`LINA_SPIKE_RUNNER` must name the scenario binary built from the accepted commit;
the checker calls its existing `-check-evidence` mode for every spike attachment.
It never relaxes the spike bounds or substitutes a headless result for the
owner's physical-terminal checks.
