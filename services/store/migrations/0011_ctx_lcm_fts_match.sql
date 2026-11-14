-- LCM-X's full-text MATCH through the tsvector indexes (lina#704). Agents read 0007's FTS views as lina_app, under
-- row-level security, and Postgres uses no index for a condition whose operator is not leakproof (tsvector @@
-- tsquery is not) ahead of the row policies: every MATCH tokenized every row of its table (5 s for 30k messages,
-- so a large store overran lcm_recall's budget). ctx.lcm_fts_match runs the MATCH with its owner's rights, so the expression indexes of
-- 0007 serve it, and returns only the calling agent's rows (lina.agent_id), the rows its policies on those tables
-- show it: its own LCM rows, over message text of its own or shared with every agent. Rollback: DROP FUNCTION.
CREATE OR REPLACE FUNCTION ctx.lcm_fts_match(fts_table text, query tsquery) RETURNS SETOF bigint
    LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    CASE fts_table
    WHEN 'messages_fts' THEN
        RETURN QUERY SELECT p.store_id FROM ctx.lcm_messages AS p JOIN memory.messages AS m ON m.uid = p.text_uid
            WHERE p.agent_id = public.lina_current_agent()
              AND (m.agent_id = public.lina_current_agent() OR m.scope = 'agents')
              AND to_tsvector('simple', ctx.lcm_unicode61(m.content)) @@ query;
    WHEN 'messages_fts_stem' THEN
        RETURN QUERY SELECT p.store_id FROM ctx.lcm_messages AS p JOIN memory.messages AS m ON m.uid = p.text_uid
            WHERE p.agent_id = public.lina_current_agent()
              AND (m.agent_id = public.lina_current_agent() OR m.scope = 'agents')
              AND to_tsvector('ctx.lcm_porter', ctx.lcm_unicode61(m.content)) @@ query;
    WHEN 'nodes_fts' THEN
        RETURN QUERY SELECT n.node_id FROM ctx.summary_nodes AS n
            WHERE n.agent_id = public.lina_current_agent()
              AND to_tsvector('simple', ctx.lcm_unicode61(n.summary)) @@ query;
    WHEN 'lcm_trajectory_states_fts' THEN
        RETURN QUERY SELECT s.state_id FROM ctx.lcm_trajectory_states AS s
            WHERE s.agent_id = public.lina_current_agent()
              AND to_tsvector('simple', ctx.lcm_unicode61(s.search_text)) @@ query;
    ELSE
        RAISE EXCEPTION 'ctx.lcm_fts_match has no full-text table %', fts_table USING ERRCODE = 'invalid_parameter_value';
    END CASE;
END $$;
REVOKE ALL ON FUNCTION ctx.lcm_fts_match(text, tsquery) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION ctx.lcm_fts_match(text, tsquery) TO lina_app;
