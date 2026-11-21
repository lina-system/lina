CREATE OR REPLACE FUNCTION memory.record_standing_rule(
    p_rule_id uuid, p_source_uid text, p_kind text, p_scope text, p_agent_id text, p_place_ref text,
    p_statement text, p_status text DEFAULT 'active', p_cites text[] DEFAULT '{}', p_filter_match jsonb DEFAULT NULL
) RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    caller text := public.lina_current_agent();
    recorder text := 'agent:' || caller;
    source_date timestamptz;
    prior memory.standing_rules%ROWTYPE;
    next_version integer;
BEGIN
    SELECT coalesce(m.ts, m.observed_at) INTO source_date FROM memory.messages m
        WHERE m.uid = p_source_uid AND m.source = 'agent' AND m.role = 'user'
          AND m.agent_id = caller AND nullif(m.session_id, '') IS NOT NULL
          AND m.speaker = 'owner' AND m.grade = 'owner' AND m.forgotten_at IS NULL FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'standing rule needs an owner turn from a session of the recording agent'
            USING ERRCODE = 'check_violation';
    END IF;
    IF EXISTS (SELECT FROM unnest(p_cites) AS c(uid) WHERE NOT EXISTS (
        SELECT FROM memory.messages m WHERE m.uid = c.uid AND m.agent_id = caller
            AND m.forgotten_at IS NULL AND m.grade <> 'external')) THEN
        RAISE EXCEPTION 'standing rule citation is unavailable or external' USING ERRCODE = 'check_violation';
    END IF;

    -- A row lock alone cannot serialize two first versions, when there is no row yet.
    PERFORM pg_advisory_xact_lock(hashtextextended(p_rule_id::text, 0));
    SELECT * INTO prior FROM memory.standing_rules WHERE rule_id = p_rule_id ORDER BY version DESC LIMIT 1;
    -- The target controls visibility, but the agent that recorded the latest version can also revise it.
    IF FOUND AND prior.agent_id NOT IN ('*', recorder) AND prior.recorded_by <> recorder THEN
        RAISE EXCEPTION 'standing rule unavailable' USING ERRCODE = 'insufficient_privilege';
    END IF;
    IF p_status NOT IN ('active', 'ended') OR prior.status = 'forgotten'
       OR (p_status = 'ended' AND prior.status IS DISTINCT FROM 'active') THEN
        RAISE EXCEPTION 'invalid standing rule transition' USING ERRCODE = 'check_violation';
    END IF;
    next_version := coalesce(prior.version, 0) + 1;
    UPDATE memory.standing_rules SET status = 'replaced', replaced_by = next_version, replaced_at = source_date
        WHERE rule_id = p_rule_id AND status = 'active';
    INSERT INTO memory.standing_rules (rule_id, version, kind, scope, agent_id, place_ref, statement,
                                        source_uid, cites, said_at, recorded_by, status, filter_match, filtered_count)
    VALUES (p_rule_id, next_version, p_kind, p_scope, p_agent_id, p_place_ref, p_statement, p_source_uid,
            p_cites, source_date, recorder, p_status, p_filter_match, CASE WHEN p_kind = 'filter' THEN 0 END);
    RETURN next_version;
END $$;

-- Listing remains subject to RLS. The original recorder can also address a
-- rule it recorded for another agent, until a different recorder takes over.
CREATE OR REPLACE FUNCTION memory.standing_rule_head(p_rule_id uuid)
RETURNS SETOF memory.standing_rules
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
    SELECT head.* FROM (
        SELECT * FROM memory.standing_rules WHERE rule_id = p_rule_id ORDER BY version DESC LIMIT 1
    ) head WHERE head.agent_id IN ('*', 'agent:' || public.lina_current_agent())
              OR head.recorded_by = 'agent:' || public.lina_current_agent()
$$;
REVOKE ALL ON FUNCTION memory.standing_rule_head(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.standing_rule_head(uuid) TO lina_app;

-- No copy of the statement survives forgetting its source. Matching rule
-- IDs and private provenance from other profiles never leave the store.
CREATE TABLE memory.rule_offers (
    offer_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id text NOT NULL,
    session_id text NOT NULL,
    anchor_rule uuid,
    pattern_uid text UNIQUE,
    fingerprint bytea NOT NULL UNIQUE,
    question_hash bytea NOT NULL,
    evidence text[] NOT NULL,
    members jsonb NOT NULL,
    owner_message_id bigint NOT NULL,
    after_message_id bigint NOT NULL,
    decision text CHECK (decision IN ('recorded', 'declined', 'stale')),
    answer_uid text UNIQUE,
    result_rule uuid,
    CHECK (num_nonnulls(anchor_rule, pattern_uid) = 1),
    UNIQUE (agent_id, session_id, owner_message_id)
);
REVOKE ALL ON memory.rule_offers FROM PUBLIC, lina_app;
ALTER TABLE memory.rule_offers ENABLE ROW LEVEL SECURITY;
ALTER TABLE memory.rule_offers FORCE ROW LEVEL SECURITY;
ALTER POLICY lina_agent_rows ON memory.rule_offers TO lina_app
    USING (agent_id = public.lina_current_agent()) WITH CHECK (false);
GRANT SELECT (offer_id, agent_id, session_id, anchor_rule, pattern_uid) ON memory.rule_offers TO lina_app;
CREATE INDEX rule_offer_journal ON memory.messages (agent_id, message_id DESC)
    WHERE source = 'heart' AND forgotten_at IS NULL AND grade IN ('owner', 'agent');

ALTER TABLE memory.standing_rules ADD COLUMN shared_rule_id uuid;
-- Locate the unnamed successor constraint from 0026 by its column. A
-- replacement now points either to the next local version or a shared birth.
DO $$
DECLARE successor_check name;
BEGIN
    SELECT conname INTO STRICT successor_check FROM pg_constraint
    WHERE conrelid = 'memory.standing_rules'::regclass AND contype = 'c'
      AND (SELECT attnum FROM pg_attribute WHERE attrelid = conrelid AND attname = 'replaced_by') = ANY(conkey);
    EXECUTE format('ALTER TABLE memory.standing_rules DROP CONSTRAINT %I', successor_check);
END $$;
ALTER TABLE memory.standing_rules ADD CONSTRAINT standing_rule_successor CHECK (
    CASE WHEN status = 'replaced' THEN replaced_at IS NOT NULL AND replaced_by IS NOT NULL AND
        CASE WHEN shared_rule_id IS NULL THEN replaced_by = version + 1
             ELSE shared_rule_id <> rule_id AND replaced_by = 1 END
    ELSE replaced_at IS NULL AND replaced_by IS NULL AND shared_rule_id IS NULL END);

CREATE FUNCTION memory.forget_rule_successor() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    NEW.shared_rule_id := NULL;
    RETURN NEW;
END $$;
CREATE TRIGGER forget_rule_successor BEFORE UPDATE OF forgotten_at ON memory.standing_rules
FOR EACH ROW WHEN (NEW.forgotten_at IS NOT NULL AND OLD.shared_rule_id IS NOT NULL)
EXECUTE FUNCTION memory.forget_rule_successor();

-- Invoker security. Under the definer this may match other agents' rules,
-- but only if their recording agent still has the original owner sentence.
CREATE FUNCTION memory.rule_offer_members()
RETURNS SETOF memory.standing_rules
LANGUAGE sql STABLE SET search_path = pg_catalog, pg_temp AS $$
    SELECT r.* FROM memory.standing_rules r JOIN memory.messages m ON m.uid = r.source_uid
    WHERE r.scope = 'agent' AND r.status = 'active' AND r.recorded_by = 'agent:' || m.agent_id
      AND m.source = 'agent' AND m.role = 'user' AND m.speaker = 'owner' AND m.grade = 'owner'
      AND m.forgotten_at IS NULL AND nullif(m.session_id, '') IS NOT NULL
      AND NOT EXISTS (SELECT FROM memory.message_parts WHERE message_id = m.message_id AND kind = 'reply_quote')
      AND NOT EXISTS (SELECT FROM unnest(r.cites) c(uid) WHERE NOT EXISTS (
          SELECT FROM memory.messages e WHERE e.uid = c.uid AND e.agent_id = m.agent_id
            AND e.grade <> 'external' AND e.forgotten_at IS NULL))
$$;
REVOKE ALL ON FUNCTION memory.rule_offer_members() FROM PUBLIC;

CREATE FUNCTION memory.rule_offer_input(p_rule uuid, p_pattern text)
RETURNS TABLE (statement text, kind text, filter_match jsonb, evidence text[], origin_session text)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
    SELECT r.statement, r.kind, r.filter_match, ARRAY[m.uid] || r.cites, m.session_id
    FROM memory.rule_offer_members() r JOIN memory.messages m ON m.uid = r.source_uid
    WHERE p_pattern IS NULL AND r.rule_id = p_rule AND m.agent_id = public.lina_current_agent()
    UNION ALL
    SELECT h.native->>'note', 'conduct', NULL::jsonb, ARRAY[h.uid] || e.uids, NULL
    FROM memory.messages h CROSS JOIN LATERAL (SELECT memory.cited_uids(h.native) AS uids) e
    WHERE p_rule IS NULL AND h.uid = p_pattern AND h.agent_id = public.lina_current_agent()
      AND h.source = 'heart' AND h.speaker = 'agent' AND h.grade IN ('owner', 'agent')
      AND h.forgotten_at IS NULL AND jsonb_typeof(h.native->'note') = 'string'
      AND EXISTS (SELECT FROM memory.messages m WHERE m.uid = ANY(e.uids) AND m.role = 'user')
      AND NOT EXISTS (SELECT FROM unnest(e.uids) u(uid) WHERE NOT EXISTS (
          SELECT FROM memory.messages m WHERE m.uid = u.uid AND m.agent_id = h.agent_id
            AND m.source = 'agent' AND nullif(m.session_id, '') IS NOT NULL AND m.forgotten_at IS NULL
            AND ((m.role = 'user' AND m.speaker = 'owner' AND m.grade = 'owner')
                 OR (m.role = 'assistant' AND m.speaker = 'agent' AND m.grade = 'agent'))
            AND NOT EXISTS (SELECT FROM memory.message_parts
                            WHERE message_id = m.message_id AND kind = 'reply_quote')))
$$;
REVOKE ALL ON FUNCTION memory.rule_offer_input(uuid,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.rule_offer_input(uuid,text) TO lina_app;

CREATE FUNCTION memory.rule_offer_fingerprint(p_rule uuid, p_text text, p_kind text, p_match jsonb)
RETURNS bytea LANGUAGE sql STABLE SET search_path = pg_catalog, pg_temp AS $$
    SELECT sha256(convert_to(jsonb_build_array(
        CASE WHEN p_rule IS NULL THEN public.lina_current_agent() END, p_text, p_kind, p_match)::text, 'UTF8'))
$$;

CREATE FUNCTION memory.make_rule_offer(p_rule uuid, p_pattern text, p_session text, p_checked text)
RETURNS TABLE (offer_id uuid, question text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    src record;
    versions jsonb := '{}';
    caller text := public.lina_current_agent();
    turn memory.messages%ROWTYPE;
    watermark bigint;
    prompt text;
    issued uuid;
BEGIN
    SELECT * INTO src FROM memory.rule_offer_input(p_rule, p_pattern);
    IF NOT FOUND OR p_checked IS DISTINCT FROM src.statement OR length(src.statement) NOT BETWEEN 1 AND 200
       OR btrim(src.statement) = '' OR src.statement ~ '[[:cntrl:]「」?？]' THEN RETURN; END IF;
    p_session := coalesce(p_session, src.origin_session);
    SELECT max(m.message_id) INTO watermark FROM memory.messages m
        WHERE m.agent_id = caller AND m.source = 'agent' AND m.session_id = p_session;
    SELECT * INTO turn FROM memory.messages m WHERE m.agent_id = caller AND m.source = 'agent'
        AND m.session_id = p_session AND m.message_id <= watermark AND (m.role = 'user'
        OR (m.role = 'assistant' AND coalesce(m.content, '') <> '' AND coalesce(m.tool_calls, 'null') IN ('null','[]')))
        ORDER BY m.message_id DESC LIMIT 1;
    IF NOT FOUND OR turn.role <> 'user' OR turn.speaker <> 'owner' OR turn.grade <> 'owner'
       OR turn.forgotten_at IS NOT NULL OR nullif(p_session, '') IS NULL THEN RETURN; END IF;
    IF p_rule IS NOT NULL THEN
        SELECT jsonb_object_agg(r.rule_id::text, r.version) INTO versions FROM memory.rule_offer_members() r
        WHERE r.statement = src.statement AND r.kind = src.kind AND r.filter_match IS NOT DISTINCT FROM src.filter_match
        HAVING count(DISTINCT r.agent_id) >= 2;
        IF versions IS NULL OR NOT versions ? p_rule::text THEN RETURN; END IF;
    END IF;
    prompt := format('「%s」을 %s 규칙으로 정할까요?', src.statement, CASE WHEN p_rule IS NULL THEN '제' ELSE '모두의' END);
    INSERT INTO memory.rule_offers AS o (agent_id, session_id, anchor_rule, pattern_uid, fingerprint,
        question_hash, evidence, members, owner_message_id, after_message_id)
    VALUES (caller, p_session, p_rule, p_pattern,
            memory.rule_offer_fingerprint(p_rule, src.statement, src.kind, src.filter_match),
            sha256(convert_to(prompt, 'UTF8')), src.evidence, versions, turn.message_id, watermark)
    ON CONFLICT DO NOTHING RETURNING o.offer_id INTO issued;
    IF FOUND THEN RETURN QUERY SELECT issued, prompt; END IF;
END $$;
REVOKE ALL ON FUNCTION memory.make_rule_offer(uuid,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.make_rule_offer(uuid,text,text,text) TO lina_app;

CREATE FUNCTION memory.answer_rule_offer(p_offer uuid, p_uid text, p_session text, p_checked text)
RETURNS TABLE (status text, rule_id uuid)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp AS $$
DECLARE
    o memory.rule_offers%ROWTYPE;
    src record;
    origin memory.messages%ROWTYPE;
    shown memory.messages%ROWTYPE;
    reply memory.messages%ROWTYPE;
    dialogue record;
    lines text[];
    candidate uuid;
    new_rule uuid;
    outcome text;
BEGIN
    SELECT * INTO o FROM memory.rule_offers t WHERE t.offer_id = p_offer
        AND t.agent_id = public.lina_current_agent() AND t.session_id = p_session FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    -- A rewrite keeps the placement's sequence and can even reuse an older text UID.
    -- Apply the issuance watermark to the birth of each placement, not its last write.
    WITH placements AS (
        SELECT min(m.message_id) AS first_id, max(m.message_id) AS latest_id,
               max(m.message_id) FILTER (WHERE m.uid = p_uid) AS answer_id
        FROM memory.messages m
        WHERE m.agent_id = o.agent_id AND m.source = 'agent' AND m.session_id = p_session
        GROUP BY m.seq_in_session, CASE WHEN m.seq_in_session IS NULL THEN m.message_id END
        HAVING min(m.message_id) > o.after_message_id
    ), turns AS (
        SELECT p.first_id, p.latest_id, m.role, m.content, m.tool_calls,
               lead(p.answer_id) OVER (ORDER BY p.first_id) AS next_answer_id,
               count(*) FILTER (WHERE m.role = 'user') OVER
                   (ORDER BY p.first_id ROWS BETWEEN 1 FOLLOWING AND UNBOUNDED FOLLOWING) AS remaining_users
        FROM placements p JOIN memory.messages m ON m.message_id = p.latest_id
    )
    SELECT latest_id, next_answer_id, remaining_users INTO dialogue FROM turns
        WHERE role = 'user' OR (role = 'assistant' AND coalesce(content, '') <> ''
                                AND coalesce(tool_calls, 'null') IN ('null', '[]'))
        ORDER BY first_id LIMIT 1;
    -- Tool traffic may precede the question. Its immediate successor must contain the
    -- caller's exact placement UID and be the only remaining user turn.
    IF NOT FOUND OR dialogue.next_answer_id IS NULL OR dialogue.remaining_users <> 1 THEN RETURN; END IF;
    SELECT * INTO shown FROM memory.messages WHERE message_id = dialogue.latest_id FOR SHARE;
    IF NOT FOUND OR shown.role <> 'assistant' OR shown.speaker <> 'agent' OR shown.grade <> 'agent'
       OR shown.forgotten_at IS NOT NULL THEN RETURN; END IF;
    SELECT * INTO origin FROM memory.messages m WHERE m.message_id = o.owner_message_id FOR SHARE;
    IF NOT FOUND OR coalesce(shown.ts, shown.observed_at) < coalesce(origin.ts, origin.observed_at)
       OR shown.seq_in_session <= origin.seq_in_session
    THEN RETURN; END IF;
    lines := string_to_array(rtrim(shown.content, E' \t\r\n'), E'\n');
    IF sha256(convert_to(lines[cardinality(lines)], 'UTF8')) IS DISTINCT FROM o.question_hash
       OR array_to_string(lines[1:cardinality(lines)-1], E'\n') ~ '[?？]' THEN RETURN; END IF;

    SELECT * INTO reply FROM memory.messages WHERE message_id = dialogue.next_answer_id FOR SHARE;
    IF NOT FOUND OR reply.uid IS DISTINCT FROM p_uid OR reply.role IS DISTINCT FROM 'user'
       OR reply.speaker <> 'owner' OR reply.grade <> 'owner' OR reply.forgotten_at IS NOT NULL
       OR coalesce(reply.ts, reply.observed_at) < coalesce(shown.ts, shown.observed_at)
       OR reply.seq_in_session <= shown.seq_in_session
       OR EXISTS (SELECT FROM memory.message_parts WHERE message_id = reply.message_id AND kind = 'reply_quote')
       THEN RETURN; END IF;
    IF o.decision IS NOT NULL THEN
        IF p_uid = o.answer_uid THEN RETURN QUERY SELECT o.decision, o.result_rule; END IF;
        RETURN;
    END IF;
    IF lower(btrim(reply.content)) IN ('no','no.','아니','아니요','아니요.') THEN
        outcome := 'declined';
    ELSIF lower(btrim(reply.content)) IN ('yes','yes.','네','네.','예','응','좋아') THEN
        -- Take the same version locks as record_standing_rule, in stable order,
        -- then lock consent evidence before rechecking its grade and liveness.
        FOR candidate IN SELECT key::uuid FROM jsonb_each(o.members) ORDER BY 1 LOOP
            PERFORM pg_advisory_xact_lock(hashtextextended(candidate::text, 0));
        END LOOP;
        PERFORM 1 FROM memory.messages m WHERE m.uid = ANY(o.evidence) OR m.uid IN (
            SELECT unnest(ARRAY[r.source_uid] || r.cites) FROM memory.standing_rules r
            JOIN jsonb_each_text(o.members) v(id, ver) ON r.rule_id = v.id::uuid AND r.version = v.ver::int)
            ORDER BY m.uid FOR SHARE;
        SELECT * INTO src FROM memory.rule_offer_input(o.anchor_rule, o.pattern_uid);
        IF NOT FOUND OR origin.grade <> 'owner' OR origin.speaker <> 'owner' OR origin.forgotten_at IS NOT NULL
           OR src.statement IS DISTINCT FROM p_checked OR src.evidence IS DISTINCT FROM o.evidence
           OR o.fingerprint IS DISTINCT FROM memory.rule_offer_fingerprint(
               o.anchor_rule, src.statement, src.kind, src.filter_match)
           OR EXISTS (SELECT FROM jsonb_each_text(o.members) v(id, ver) WHERE NOT EXISTS (
               SELECT FROM memory.rule_offer_members() r WHERE r.rule_id = v.id::uuid AND r.version = v.ver::int))
        THEN
            outcome := 'stale';
        ELSE
            new_rule := gen_random_uuid();
            PERFORM memory.record_standing_rule(new_rule, p_uid, src.kind,
                CASE WHEN o.anchor_rule IS NULL THEN 'agent' ELSE 'all' END,
                CASE WHEN o.anchor_rule IS NULL THEN 'agent:' || o.agent_id ELSE '*' END,
                NULL, src.statement, 'active', src.evidence, src.filter_match);
            UPDATE memory.standing_rules r SET status = 'replaced', replaced_by = 1, shared_rule_id = new_rule,
                replaced_at = coalesce(reply.ts, reply.observed_at)
            FROM jsonb_each_text(o.members) v(id, ver)
            WHERE r.rule_id = v.id::uuid AND r.version = v.ver::int AND r.status = 'active';
            outcome := 'recorded';
        END IF;
    ELSE
        RETURN;
    END IF;
    UPDATE memory.rule_offers SET decision = outcome, answer_uid = p_uid, result_rule = new_rule
        WHERE offer_id = p_offer;
    RETURN QUERY SELECT outcome, new_rule;
END $$;
REVOKE ALL ON FUNCTION memory.answer_rule_offer(uuid,text,text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.answer_rule_offer(uuid,text,text,text) TO lina_app;
