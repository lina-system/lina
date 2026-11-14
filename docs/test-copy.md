# Running a test copy of an agent

A test copy is a second agent made from an existing one: the same memory, persona, skills and cron jobs, on its own
LINA home and its own store. The real agent keeps running where it is. The copy only reads copies of the real
agent's data and can only message the chats you allow. This page is the order that worked in the lina#41 trial.

Every command below runs from a LINA source checkout (`lina store install` builds Honcho from `services/honcho`).

## 1. A separate home and a separate store

One machine runs one store: the compose project name `lina-store` is fixed (`PROJECT` in `hermes_cli/store.py`,
`name:` in `services/store/compose.yaml`). A second LINA home on a machine that already has a store would drive the
same containers. Put the copy on a machine without a LINA store of its own.

Pick a new LINA home and set it for every command that follows:

```sh
export LINA_HOME=/path/to/test-home HERMES_HOME=/path/to/test-home
lina store install
lina store status
```

The store's settings land in `$LINA_HOME/store/` ([store.md](store.md#installing)). Nothing in this home points
at the real agent's store.

Bring the real agent's data to this machine as copies: its old home (or a copy of it) and a logical `pg_dump` archive
of its Honcho database (the trial used `pg_dump -Fc`). `lina migrate memory` never writes `--from` and never talks to
a live Honcho.

## 2. Copy the memory

```sh
lina migrate memory --from /path/to/old-home --source-profile <profile> --as <copy name> \
  --work-dir /path/to/migration-work --honcho-dump /path/to/honcho.dump
```

Add `--spool` and `--work-db` when the old agent has collected messages or a work record. `--as` must be a new name,
`--home` (the default) must be the running home, and the work directory must sit outside both homes. The copy is
`$LINA_HOME/profiles/<copy name>`. Read `migration-report.json` in the work directory: `ok` and `complete` must both
be `true`. Details: [store.md, Copying legacy memory into a new agent](store.md#copying-legacy-memory-into-a-new-agent).

## 3. Copy the cron jobs

```sh
lina migrate cron --from /path/to/old-home --source-profile <profile> --as <copy name> \
  --work-dir /path/to/migration-work
```

Run it after `migrate memory`, into the same agent. Every job lands paused with `deliver: local`. Scripts are not
copied: the report (`cron-migration-report.json`) says which jobs have a script that does not resolve. Copy those
scripts by hand into `$LINA_HOME/profiles/<copy name>/scripts/`; a job's script must be inside that folder. Details:
[store.md, Copying legacy cron jobs](store.md#copying-legacy-cron-jobs).

## 4. Model and embedding names

The copied `config.yaml` (`$LINA_HOME/profiles/<copy name>/config.yaml`) carries only memory settings and the
context engine lines. Add the model and the memory provider yourself:

```yaml
model:
  provider: magpie
  default: <remote>/<provider>/<id>   # an id exactly as GET http://127.0.0.1:3425/v1/models lists it
memory:
  provider: lina-memory               # keep the memory settings already there
```

On a node, magpie names a model in its catalog form `<remote>/<provider>/<id>` and refuses the bare id. The magpie
key goes in the profile's `.env` as `MAGPIE_API_KEY`.

Set `embedding:` in the new home's `config.yaml` with `embedding.model` in magpie's catalog form, and the same name
as `EMBEDDING_MODEL_CONFIG__MODEL` in `store/honcho.env` ([store.md, Configuration](store.md#configuration)).
Then embed the copied memory in the order store.md gives (`lina store reembed --source memory`, then
`--source agent`). Until it finishes, search over the copy is partial and `lina doctor` lists the pending
conclusions under Memory Vectors.

## 5. Let Honcho reach its models

Honcho runs in Docker, so `127.0.0.1` in `honcho.env` is the container, not the machine. Give it a way to the
models as [store.md, Reaching models from Honcho](store.md#reaching-models-from-honcho) says. Without it, Honcho
cannot write the copy's new chats (in the trial it answered them with HTTP 500). `lina doctor` shows the check
under Memory Store.

## 6. The outbound allowlist

Before the copy talks to anyone, limit where its messages may go. In the copy's `config.yaml`:

```yaml
outbound:
  allow:
    - telegram:<the owner's test chat id>
    - agent:<copy name>
```

`agent:<copy name>` lets the copy's own worker end notices through: they are checked like any other agent target.
Every other destination is refused, the refusal names `outbound.allow`, and `lina doctor` counts it
(`outbound_refusals.json` in the profile home). An unset key or an empty list means no limit, so never leave it
out. Details: [the outbound allowlist](design/message-routing.md#outbound-allowlist-built-lina638).

Messaging credentials are not copied. Give the copy its own test bot: two profiles that hold one bot token collide.

## 7. The profile's own API server key

`lina migrate memory` writes a fresh `API_SERVER_KEY` into the copy's `.env`, and the same value as
`LINA_GATEWAY_KEY_<COPY NAME>` (upper case, `-` as `_`) into the LINA home's `.env`, where the web server reads it.
Never copy the real agent's key into the copy. `lina doctor` says "Every agent's gateway key matches the web
server's" or names the profile whose two keys differ.

## 8. What to check

Start a gateway from the copy's home (`lina gateway run`, or `lina gateway install` for a service), and check with
`lina gateway list` that it serves the copy. Then, in this order:

1. **One chat.** `lina -p <copy name> chat -q "<a question>" --oneshot`, or one message in the app or the test chat.
   The copy answers on the model you set.
2. **Recall in the next chat.** In a new chat, ask about something from the first one, and about something from the
   real agent's past. The copy finds both (partial until step 4's re-embedding is done).
3. **One worker call.** Write a small brief, then:

   ```sh
   lina -p <copy name> worker submit --brief brief.md --topic test-copy --wait 1800
   lina worker result <ID>
   ```

   The end notice reaches the copy, not the real agent.
4. **One dry-run cron.** `lina -p <copy name> cron list` for an id, then `lina -p <copy name> cron run <id> --dry-run`.
   It resolves the job and runs, calls and sends nothing; `lina -p <copy name> cron runs <id>` shows it as a dry run.
5. **Restart.** `lina gateway restart` for a service, or stop and start `lina gateway run`. The earlier chat's
   history is still there and a new message is answered.

`lina doctor` should end with no store, model, key or vector warnings you did not expect.

## 9. What a copy cannot do yet

- **Cron scripts** are not copied with the jobs (step 3).
- **The cron dry run does not check `outbound.allow`.** Its delivery part says `present but not wired into the dry
  run`; the allowlist still holds when a job really runs.
- **The model and memory provider** are not set by the migration (step 4).
- **The embedding model is named in two places**, `embedding.model` and `honcho.env`, and both must match.
- **`lina store reembed` does not retry on HTTP 429.** On a shared magpie a large `--source agent` run can stop
  part way. Run it again later; it continues where it stopped (`--max-batches` keeps one run short).
- **One store per machine** (step 1).

## 10. Tearing it down

On the copy's machine only:

1. Stop the copy's gateway: `lina gateway stop` (and `lina gateway uninstall` if you installed it as a service).
2. Stop the store. No `lina store` command removes it; use the same compose call `hermes_cli/store.py` makes:

   ```sh
   docker compose -p lina-store -f services/store/compose.yaml --env-file "$LINA_HOME/store/store.env" down
   ```

   Then remove its data volume, named by `LINA_STORE_VOLUME` in `store.env` (default `lina-store-pgdata`), and any
   volume `lina store restore` printed: `docker volume rm <name>`.
3. Undo what you did for step 5: delete the firewall rule (`iptables -D` with the same arguments as the `-I`) and
   stop any forwarder in front of magpie.
4. Delete the copy's LINA home and the migration work directory.
