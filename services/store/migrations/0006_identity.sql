-- The identity registry (lina#20): stable ids for people, agents and conversations, and the source keys each came
-- from. lina-memory resolves every finished turn through it and fills memory.messages.conversation_id from it.
-- Neither table has an agent_id column: a person or a conversation can span agents, so row-level security does not
-- apply here (docs/store.md).

CREATE TABLE IF NOT EXISTS memory.identities (
    -- person:<hash>, agent:<profile>, conv:<hash>; external message rooms keep their own id, messages:<platform>:<room id> (#18).
    identity_id text PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('person', 'agent', 'conversation')),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS memory.identity_keys (
    kind text NOT NULL CHECK (kind IN ('person', 'agent', 'conversation')),
    -- gateway_user: <platform>:<user id>; local_user: the machine owner on local surfaces; profile: <profile>;
    -- gateway_chat: <profile>:<gateway session key>; hermes_session: <profile>:<session id>;
    -- lcm_conversation: <profile>:<LCM conversation id>; honcho_peer: <workspace>:<peer id>;
    -- honcho_session: <workspace>:<session key>; messages_room and messenger_author belong to external messages (#18).
    source text NOT NULL CHECK (source IN ('gateway_user', 'local_user', 'profile', 'gateway_chat', 'hermes_session',
                                           'lcm_conversation', 'honcho_peer', 'honcho_session', 'messages_room',
                                           'messenger_author')),
    key text NOT NULL,
    identity_id text NOT NULL REFERENCES memory.identities (identity_id),
    first_seen timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, source, key)
);

CREATE INDEX IF NOT EXISTS identity_keys_identity ON memory.identity_keys (identity_id);
CREATE INDEX IF NOT EXISTS messages_conversation ON memory.messages (conversation_id) WHERE conversation_id IS NOT NULL;
-- MessageStore.set_conversation finds a turn's rows, every text version of each (<base uid>#<digest>), by base uid while
-- their conversation is unfilled. The uid's own unique index cannot serve that: under the store's en_US collation a
-- btree on uid answers no LIKE prefix match. Filled rows leave this index.
CREATE INDEX IF NOT EXISTS messages_unfilled_base ON memory.messages (split_part(uid, '#', 1)) WHERE conversation_id IS NULL;
