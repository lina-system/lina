-- LCM-X's LIKE search (Korean, Chinese, Japanese, emoji queries) through the trigram index (lina#711). The statement
-- reads ctx.messages as lina_app, under row-level security: Postgres uses no index for ILIKE (not leakproof) ahead of
-- the row policies, and the view's content is an expression over memory.messages.native that it computed again for
-- every reference, so a recall over 160k messages scanned them all and ranked the matches for 27 s.
-- ctx.lcm_like_match returns the calling agent's messages whose content (the view's) matches any of the ILIKE
-- *patterns* (escape character '\'), with its owner's rights: when every pattern holds a run of three word
-- characters (one pg_trgm trigram at least), from messages_content_trgm per pattern, else from one scan that lowers
-- each text once. It may return more rows than match; the caller keeps its own ILIKE on the rows it gets.
-- Rows whose content comes from native (content_raw) are found through messages_content_raw.
-- Rollback: DROP FUNCTION ctx.lcm_like_match(text[]); DROP INDEX memory.messages_content_raw;
CREATE INDEX IF NOT EXISTS messages_content_raw ON memory.messages (uid) WHERE (native->'content_raw'->>0) = 's';

CREATE OR REPLACE FUNCTION ctx.lcm_like_match(patterns text[]) RETURNS SETOF bigint
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    agent text := public.lina_current_agent();
    lowered text[] := ARRAY(SELECT lower(p) FROM unnest(patterns) AS p);
BEGIN
    IF cardinality(patterns) > 0 AND (SELECT bool_and(p ~ '[[:alnum:]]{3}') FROM unnest(patterns) AS p) THEN
        RETURN QUERY
            SELECT p.store_id FROM unnest(patterns) AS pat
            JOIN LATERAL (SELECT m.uid FROM memory.messages AS m
                          WHERE m.content ILIKE pat AND (m.agent_id = agent OR m.scope = 'agents')) AS hit ON true
            JOIN ctx.lcm_messages AS p ON p.text_uid = hit.uid AND p.agent_id = agent
            UNION
            SELECT p.store_id FROM memory.messages AS m JOIN ctx.lcm_messages AS p ON p.text_uid = m.uid
            WHERE (m.native->'content_raw'->>0) = 's' AND p.agent_id = agent
              AND (m.agent_id = agent OR m.scope = 'agents') AND lower(m.native->'content_raw'->>1) LIKE ANY (lowered);
    ELSE
        RETURN QUERY
            SELECT p.store_id FROM ctx.lcm_messages AS p JOIN memory.messages AS m ON m.uid = p.text_uid
            WHERE p.agent_id = agent AND (m.agent_id = agent OR m.scope = 'agents')
              AND lower(CASE WHEN m.native->'content_raw'->>0 = 's' THEN m.native->'content_raw'->>1 ELSE m.content END)
                  LIKE ANY (lowered);
    END IF;
END $$;
REVOKE ALL ON FUNCTION ctx.lcm_like_match(text[]) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ctx.lcm_like_match(text[]) TO lina_app;
