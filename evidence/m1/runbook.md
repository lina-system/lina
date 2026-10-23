# Owner run: LINA 1

This procedure has not been run. Use a disposable supported host or dedicated
test login, a fresh private state root, synthetic conversation data and one
clean commit already on `dev`. The person's assessment is UI/UX; the headless
suite supplies the machine-observable behavior and fault checks. Do not repeat
dangerous fault injections against a personal installation.

## Prepare one commit

Use a checkout of the chosen dev commit. Do not change it during the run.
Fetch `origin/dev` before choosing the commit; the checker uses that local ref
and does not fetch or write Git refs itself.

```sh
git fetch origin dev
git status --porcelain
git rev-parse HEAD
git merge-base --is-ancestor HEAD origin/dev
M1_OUTPUT=$(mktemp -d "${TMPDIR:-/tmp}/lina-m1.XXXXXX")
chmod 700 "$M1_OUTPUT"
dv heavy -- node scripts/build.mjs --out "$M1_OUTPUT/bundle"
dv heavy -- node scripts/accept-m1.mjs --run-headless "$M1_OUTPUT"
go test -c -o "$M1_OUTPUT/spike-runner" ./internal/tui/spike/run
export LINA_SPIKE_RUNNER="$M1_OUTPUT/spike-runner"
```

The status command must print nothing; every other command must succeed.
Build output must include a manifest. The headless command prints `commit`,
`run_id`, `headless_sha256` and `passed`. Keep that identity for this attempt.
It runs `go test -json -count=1 ./e2e/m1/... ./internal/tui/ui`, verifies required
test and package completions, and removes text output from the saved receipt.
The [acceptance map](../../e2e/m1/README.md) names the assertions and fault seams.
A missing or skipped required test, failed package or nonzero command exit is
not a pass. Failed runs retain a receipt and exit nonzero.

Hash attachments without changing their bytes. For example:

```sh
M1_MANIFEST_HASH=$(node --input-type=module -e \
  'import {readFileSync} from "node:fs"; import {createHash} from "node:crypto"; console.log(createHash("sha256").update(readFileSync(process.argv[1])).digest("hex"))' \
  "$M1_OUTPUT/bundle/manifest.json")
cp "$M1_OUTPUT/bundle/manifest.json" "$M1_OUTPUT/$M1_MANIFEST_HASH.json"
```

Use this exact-byte hashing method for the owner-session receipt and original
spike files too. Keep all artifacts private until checking repository hygiene.

## Install and inspect the UI

On the disposable Linux login, install the bundle into a previously absent
state root. Installation starts the user service. Other supported hosts use
their installation flow; record only a host code, never a hostname or address.

```sh
M1_ROOT="$M1_OUTPUT/state"
"$M1_OUTPUT/bundle/lina" install --bundle "$M1_OUTPUT/bundle" --state-root "$M1_ROOT"
"$M1_OUTPUT/bundle/lina" doctor --state-root "$M1_ROOT"
"$M1_OUTPUT/bundle/lina" tui --socket "$M1_ROOT/system/run/client.sock"
```

Observe the first READY epoch and assign an opaque root id; do not put the path
in the record. Check the no-account sign-in offer, then sign in through the
product with the owner's account. Use synthetic prompts. Inspect streaming,
long-result cards, Waiting for me, running work and notice placement. Pair the
one owner Telegram chat through the product and inspect that interaction too.
Record a UI/UX verdict; absence of an actual sign-in or channel session is
incomplete evidence, even when recorded-wire tests passed.

Follow the [terminal runbook](../tui-spike/runbook.md) for every confirmed
terminal and S1–S11. Use the same source revision and pins. Copy the actual JSON
files under their hashes; do not edit a measured result. Assign opaque ids to
the confirmed terminals and record every terminal/scenario pair. Missing
physical measurements, unconfirmed terminals, `gap`, `fail` or `not_run` leave
acceptance incomplete. The existing spike runner re-evaluates its declared
bounds when the final record is checked.

## Record the serial restart and restore matrix

Record `fresh`, then `planned_restart`, then `crash_restart`, then `restore`
from the same install lineage. Read actual protocol READY epochs; do not infer
them by adding one. Record no repeated effects after either restart. Use the
product's restart request for the planned restart so its idle fence applies.
On the disposable Linux service, the crash operation is:

```sh
systemctl --user kill --signal=SIGKILL lina-core.service
systemctl --user start lina-core.service
```

The matrix is serial. Startup fault tests use independent private roots and
check their own epoch bounds; do not concatenate those independent counters
into this matrix. Their required subtest results belong in the headless receipt.

Create a generation containing a synthetic item before forgetting it. Stop
Core before using the backup CLI, then resume it to forget the item. Keep the
generation beside its current removals file. Stop Core again and copy the
stopped installation into another private state root for the **same main**;
do not initialize another main identity. Restore the older generation there:

```sh
systemctl --user stop lina-core.service
"$M1_OUTPUT/bundle/lina" backup --state-root "$M1_ROOT"
# After resuming, forgetting the synthetic item and stopping Core again:
M1_RESTORE="$M1_OUTPUT/restored"
cp -a "$M1_ROOT" "$M1_RESTORE"
# Set M1_GENERATION to the generation path printed by backup, in the original root.
"$M1_OUTPUT/bundle/lina" restore --generation "$M1_GENERATION" --state-root "$M1_RESTORE"
"$M1_OUTPUT/bundle/lina" core --state-root "$M1_RESTORE" --socket "$M1_RESTORE/system/run/client.sock"
```

The last command runs in the foreground; connect another TUI to that socket.
Record the whole generation id, the different root id, the higher READY epoch
and the zero-exposure results. The automated older-generation scenario checks
search, summaries and request context with a retained-memory positive control;
the owner need not inspect model payloads. Stop the foreground Core with Ctrl+C.
Copying a stopped install is test setup, not an export of personal state.

## Diagnosis and final check

Collect separate model, task-interruption and storage-failure event chains from
the running install as required by
[diagnosis acceptance](../../docs/design/self-diagnosis-log.md#diagnosis-acceptance).
Use a synthetic task for each injection. Retain its independently observed root
id, task-id query filter and LINA's structured citation. Supply only the twelve
allowed event fields. The checker compares ids, class and cause codes and
follows all chain links; it never judges summary prose.

Do not replace missing running-install diagnosis with a hand-written citation
or a package-test event. The current M1 map has no installed diagnosis scenario;
if the chosen revision cannot collect this evidence, record it as `not_run` and
leave #123 open. The owner UI/UX run does not authorize a new product feature.

Create `record.json` using the [schema](record.schema.json) and
[field guide](README.md#record-format-1). Add all nine acceptance results and
all fourteen failure results; passing behavior rows reference this attempt's
headless hash. Create the owner-session receipt only after that session occurs.
Then run:

```sh
node scripts/accept-m1.mjs "$M1_OUTPUT/record.json"
```

Require exit 0 and `accepted: true`. A schema-only pass, successful spike command
with a recorded failing result, missing evidence or an old commit is insufficient.
If anything fails, retain this attempt with explicit failed/unrun cells. Fix on
`dev`, rebuild, rerun the entire set from a fresh root and append a new attempt.
Do not amend only the failed row or discard the earlier failure. The final
attempt must have every result passing and new evidence from its fixing commit.

Stop only the processes and service created for this run. Keep private records
until disposition is agreed; remove the temporary runner after validation.
Before submitting evidence, retain only ids, codes, counts, hashes and approved
synthetic spike records. Never submit state roots, secrets or personal text.
