CREATE TABLE memory.taste_voice_claims (
    agent_id text NOT NULL,
    scope text NOT NULL CHECK (scope IN ('week', 'task')),
    budget_key text NOT NULL,
    turn_id text NOT NULL,
    spent boolean NOT NULL DEFAULT false,
    taste_ids bigint[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (agent_id, scope, budget_key),
    -- Only a pending weekly recap needs references to tastes.
    CHECK (cardinality(taste_ids) <= 3),
    CHECK (cardinality(taste_ids) = 0 OR (scope = 'week' AND NOT spent))
);

CREATE INDEX taste_voice_claims_pending_turn
    ON memory.taste_voice_claims (agent_id, turn_id) WHERE NOT spent;

CREATE TABLE memory.taste_voice_measurements (
    agent_id text NOT NULL,
    turn_id text NOT NULL,
    task_kind text NOT NULL,
    length integer NOT NULL CHECK (length >= 0),
    steps text[] NOT NULL,
    disliked boolean NOT NULL DEFAULT false,
    flags jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (agent_id, turn_id)
);

CREATE INDEX taste_voice_measurements_task
    ON memory.taste_voice_measurements (agent_id, task_kind);

SELECT public.lina_apply_agent_rls('memory.taste_voice_claims');
SELECT public.lina_apply_agent_rls('memory.taste_voice_measurements');
