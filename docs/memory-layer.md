# The memory layer (`lina-memory`)

`lina-memory` (`plugins/memory/lina/`) is the memory provider in the core's one memory-provider slot
(`memory.provider: lina-memory`). It receives every finished turn once and hands it to the engines with shared
identities: LCM-X, the context engine ([lcm-x.md](lcm-x.md)), and Honcho, the long-term memory
(`plugins/memory/honcho/`, now an adapter `lina-memory` drives). New homes get it from `cli-config.yaml.example`,
the way they get `context.engine: lcm-x`; a home without the key keeps the built-in store only.

## One receive path

Each finished turn of an agent that carries `lina-memory` is received once and routed by the policy table:

1. LCM-X's own `post_llm_call` hook stays registered for every agent. `lina-memory` claims the session of its agent
   (`plugins/memory/lina/lcm.py`, `claim_session`), and the context-engine loader
   (`plugins/context_engine/__init__.py`) has the hook hand a claimed session's turn to `lina-memory` instead of
   ingesting it itself. `lina-memory` works out the route (below) and, when the policy table lets the route into
   LCM, hands the turn to the engine bound to the session, found through LCM-X's `engine_registry`: `ingest`
   (cursor-based, so rows it already holds are skipped), then `note_turn_complete`. This runs on the turn thread,
   where the hook runs, with the unredacted transcript LCM-X keeps on this machine, so the ingest never overlaps
   the next turn's own use of the engine (compaction, `lcm_*` tool calls). An LCM failure is logged with its
   traceback and does not stop the Honcho delivery below.
   An agent without `lina-memory` (a `skip_memory` delegate child, a background review, the curator) claims no
   session, and LCM-X's hook ingests its turns as before. A stopped turn is LCM-X's own: `on_turn_complete`
   stores it, or keeps an open user row for the next completed turn. Either way a turn is one ingest.
2. `MemoryManager.sync_all` then calls `lina-memory`'s `sync_turn`. Because it names a `local_messages`
   parameter, the manager calls it on the turn thread with the unredacted transcript as well as the redacted one.
   It builds one turn record: person, agent and conversation ids from the identity registry, the
   `memory.messages` uids of the turn's rows, the route and whose words the text is, the platform, the session,
   and the first and last timestamps.
3. On `lina-memory`'s own worker, one turn after another, it hands the same record to the Honcho adapter's
   `sync_turn`, when the route allows Honcho (the record, this step and the next run there). The adapter writes
   the turn as before (peers, sessions, a bot author's own a2a session) and puts the record's ids on each
   message's metadata (`lina_person`, `lina_agent`, `lina_conversation`, `lina_uid`, `lina_route`).
   Who wrote the turn and its Honcho session are fixed on the turn thread when the turn finishes and handed to
   the worker: by the time the worker runs, the next turn may have started, and Honcho's own per-turn state
   (`on_turn_start`) names that turn's author.
4. It fills `memory.messages.conversation_id` for the turn's rows (`MessageStore.set_conversation`).

The LCM-X subtree is unchanged. Recall, tools and the session lifecycle (`prefetch`, `honcho_*` tools, `on_session_end`, ...) go to the Honcho
adapter when Honcho is configured. `lina-memory`'s `plugin.yaml` names the adapter (`drives: [honcho]`), so
`hermes honcho ...` stays available while `lina-memory` holds the slot, and setting Honcho up (`hermes honcho
setup`, `hermes memory setup`) keeps `lina-memory` there.

## The policy table

`plugins/memory/lina/policy.yaml` is data: per route, whether the turn goes to LCM, to Honcho, and as whose
words. A turn's route comes from where it arrived, never from its text:

| Signal | Set by |
| --- | --- |
| `agent_context`, `platform` | the agent build (`cron`, `subagent`, `flush`, the gateway platform) |
| `display_kind` of the turn's user row | the gateway (`internal=True` events), the TUI and the CLI for self-injected notices |
| `compressor_row` | the context compressor's own rows (`_compressed_summary`, the in-flight task restatement flag) |
| `author_is_bot` | the bot relay (`turn_author`) |

Only `person_channel` turns are stored in Honcho as the person's words. A bot relay goes to Honcho as the bot's
own words in its own session; worker events, cron and background notices, gateway-internal notices and the
compressor's rows stay out of Honcho. The table's `replaces` rows list what Honcho's old text filter
(`_INTERNAL_GATEWAY_TURN_RE`) matched and the route each of those notices now arrives on; the filter is gone.
A person who types or quotes one of those strings is a person: their message reaches Honcho as their words.

`memory.provider: honcho` still loads the Honcho provider on its own; it applies the same table to the route
the manager's `messages` and its init context show.

## The identity registry

Migration `services/store/migrations/0006_identity.sql` adds `memory.identities` and `memory.identity_keys`:
one stable id per person (`person:<hash>`), agent (`agent:<profile>`) and conversation (`conv:<hash>`), with
the source keys each came from:

| Kind | Source keys |
| --- | --- |
| person | `gateway_user` (`<platform>:<user id>`), `local_user` (the machine owner on CLI/TUI), `honcho_peer` (`<workspace>:<peer>`) |
| agent | `profile`, `honcho_peer` (`<workspace>:<aiPeer>`) |
| conversation | `gateway_chat` (`<profile>:<gateway session key>`), `lcm_conversation` (`<profile>:<LCM conversation id>`), `honcho_session` (`<workspace>:<session key>`), `hermes_session` when none of those exists |

An id is derived from the first key it is registered under, so it is the same on every machine and run, before
and after the registry holds it; Honcho peer names and LCM conversation ids are read as Honcho and LCM-X derive
them today and are never renamed. A later key joins the identity an earlier key already has; two keys that name
different identities are never merged. External messages (#18) keep their conversation ids
(`messages:<platform>:<room id>`) and own the `messages_room` and `messenger_author` source kinds; their authors
are not resolved to people. The tables have no `agent_id` column, so row-level security does not apply to them.

## Measuring the cache

`lina memory cache-report --usage <usage.jsonl> [--from ISO --to ISO] [--conversation <id> ...] [--baseline <pct> ...]
[--fingerprints <file>] [--format md|tsv]` computes the lina#17 baseline's numbers from OCX usage rows only:
the token-weighted hit rate `sum(cacheReadInputTokens) / sum(inputTokens)` (OCX's input total includes reads and
writes), the per-request mean and median, unreported rows apart (never a miss or a zero), cache writes by tier, and
the cache retreat `max(0, previous_input_total - current_cache_read)` along each conversation. A gap to a baseline
within two standard errors of the rate (linearized ratio estimator over the metered requests) is reported as within
noise. Conversations are numbered; no id, endpoint or account from the input reaches the output (lina#25).

`memory.prefix_fingerprints: true` (off by default) appends one row per model request to
`<home>/state/prefix-fingerprints.jsonl`: keyed hashes and byte lengths of the request's leading blocks (tools, then
each message), never text; `--fingerprints` measures from it the exact common prefix of consecutive requests.
