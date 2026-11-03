# The memory layer (`lina-memory`)

`lina-memory` (`plugins/memory/lina/`) is the memory provider in the core's one memory-provider slot
(`memory.provider: lina-memory`). It receives every finished turn once and hands it to the engines with shared
identities: LCM-X, the context engine ([lcm-x.md](lcm-x.md)), and Honcho, the long-term memory
(`plugins/memory/honcho/`, now an adapter `lina-memory` drives). New homes get it from `cli-config.yaml.example`,
the way they get `context.engine: lcm-x`; a home without the key keeps the built-in store only.

## One receive path

`MemoryManager.sync_all` calls `lina-memory`'s `sync_turn` once per finished turn. Because its `sync_turn` names a
`local_messages` parameter, the manager calls it on the turn thread, where LCM-X's own hook ran, and hands it the
unredacted transcript as well as the redacted one. That call is the only ingest:

1. It works out the route the turn arrived on (below) and looks it up in the policy table.
2. It builds one turn record: person, agent and conversation ids from the identity registry, the
   `memory.messages` uids of the turn's rows, the route and whose words the text is, the platform, the session,
   and the first and last timestamps.
3. It hands the turn to LCM-X, there on the turn thread: the engine bound to the session, found through LCM-X's
   `engine_registry`, ingests the transcript through this turn (`ingest`, cursor-based, so rows it already holds
   are skipped), then `note_turn_complete`. LCM-X gets the transcript unredacted, as its own hook did: it keeps
   it on this machine. Running here keeps the ingest from overlapping the next turn's own use of the engine
   (compaction, `lcm_*` tool calls), which do not take the engine's lease.
4. On `lina-memory`'s own worker, one turn after another, it hands the same record to the Honcho adapter's
   `sync_turn`, when the route allows Honcho (steps 2, 4 and 5 run there). The adapter writes
   the turn as before (peers, sessions, a bot author's own a2a session) and puts the record's ids on each
   message's metadata (`lina_person`, `lina_agent`, `lina_conversation`, `lina_uid`, `lina_route`).
   Who wrote the turn and its Honcho session are fixed on the turn thread when the turn finishes and handed to
   the worker: by the time the worker runs, the next turn may have started, and Honcho's own per-turn state
   (`on_turn_start`) names that turn's author.
5. It fills `memory.messages.conversation_id` for the turn's rows (`MessageStore.set_conversation`).

LCM-X's own `post_llm_call` ingest hook is not registered while the home's memory slot holds `lina-memory`: the
context-engine loader (`plugins/context_engine/__init__.py`) leaves that one hook out. The subtree is unchanged.
Recall, tools and the session lifecycle (`prefetch`, `honcho_*` tools, `on_session_end`, ...) go to the Honcho
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

Migration `services/store/migrations/0004_identity.sql` adds `memory.identities` and `memory.identity_keys`:
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
