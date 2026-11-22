-- A journal stays consumed after its candidate and evidence expire.
CREATE TABLE memory.taste_consumed (
    agent_id text NOT NULL,
    journal_uid text NOT NULL,
    consumed_on date NOT NULL,
    PRIMARY KEY (agent_id, journal_uid)
);

SELECT public.lina_apply_agent_rls('memory.taste_consumed');
