-- How much a remembered item may be trusted (lina#440, design lina#439 section 1). The speaker (0013) says who said
-- it; the grade says how far it may be trusted: owner (the owner's own words), agent (an agent's inference from a
-- conversation with the owner, the gateway's own notes) or external (anything from outside, or of unknown origin).
-- The least trusted wins: a derived item is graded by the lowest grade of its sources. Rules: docs/store.md.

CREATE OR REPLACE FUNCTION memory.grade_of_speaker(speaker text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE speaker WHEN 'owner' THEN 'owner' WHEN 'agent' THEN 'agent' WHEN 'gateway' THEN 'agent'
                        ELSE 'external' END $$;

CREATE OR REPLACE FUNCTION memory.grade_rank(grade text) RETURNS integer
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE grade WHEN 'owner' THEN 2 WHEN 'agent' THEN 1 ELSE 0 END $$;

-- The least trusted of *grades*; none at all is external.
CREATE OR REPLACE FUNCTION memory.lowest_grade(grades text[]) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT coalesce((SELECT g FROM unnest(grades) AS g ORDER BY memory.grade_rank(g) LIMIT 1), 'external') $$;

ALTER TABLE memory.messages ADD COLUMN IF NOT EXISTS grade text;
-- Where the item comes from, as memory_open opens it: msg:<base uid> of the row itself for a message (an external
-- message's uid carries its platform message id), the cited rows (msg:<uid>, space separated) for a heart row
-- (itself when it cites none).
ALTER TABLE memory.messages ADD COLUMN IF NOT EXISTS source_id text;

-- The cited rows of a heart journal row (native.evidence), as stored.
CREATE OR REPLACE FUNCTION memory.cited_uids(native jsonb) RETURNS text[]
    LANGUAGE sql IMMUTABLE AS $$
    SELECT ARRAY(SELECT jsonb_array_elements_text(CASE WHEN jsonb_typeof(native->'evidence') = 'array'
                                                       THEN native->'evidence' ELSE '[]'::jsonb END)) $$;

CREATE OR REPLACE FUNCTION memory.default_source_id(source text, uid text, native jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN source = 'heart'
                THEN coalesce((SELECT string_agg('msg:' || u, ' ') FROM unnest(memory.cited_uids(native)) AS u),
                              'msg:' || uid)
                ELSE 'msg:' || split_part(uid, '#', 1) END $$;

-- A row's grade: its speaker's, except that
-- - the compressor's own summary row restates sources it does not name, so it is external;
-- - a heart journal row is the lowest grade of the rows it cites (a cited row this agent cannot read, that is gone
--   or whose speaker is still unknown, is external), and one whose evidence is known to be from outside (it cites a
--   row of a decided speaker that is external, and none of grade owner or agent) is refused: only owner or agent
--   items may change an agent's preferences or voice. Unknown evidence is graded, not refused: an imported trial
--   line cites imported turns whose speaker no rule can decide;
-- - a writer may set a lower grade than the rule's, never a higher one, and an external row never moves up.
-- A user turn's speaker is filled when the turn finishes (MessageStore.set_speakers), so its grade follows then.
CREATE OR REPLACE FUNCTION memory.messages_trust_grade() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    rule text;
    cited text[];
    found text[];
    outside boolean;
    asked text := NEW.grade;
BEGIN
    IF NEW.source = 'heart' THEN
        cited := memory.cited_uids(NEW.native);
        IF cardinality(cited) = 0 THEN
            rule := 'agent';
        ELSE
            SELECT array_agg(coalesce(s.grade, 'external')),
                   coalesce(bool_or(s.grade = 'external' AND coalesce(s.speaker, 'unknown') <> 'unknown'), false)
                INTO found, outside
            FROM unnest(cited) AS e(uid) LEFT JOIN memory.messages AS s ON s.uid = e.uid;
            IF outside AND NOT ('owner' = ANY(found) OR 'agent' = ANY(found)) THEN
                RAISE EXCEPTION 'heart row % cites only external items', NEW.uid
                    USING ERRCODE = 'check_violation',
                          HINT = 'Only owner or agent items may change preferences or voice (lina#440).';
            END IF;
            rule := memory.lowest_grade(found);
        END IF;
    ELSIF NEW.role = 'user' AND (NEW.native->>'_compressed_summary') = 'true' THEN
        rule := 'external';
    ELSE
        rule := memory.grade_of_speaker(NEW.speaker);
    END IF;
    IF TG_OP = 'UPDATE' AND asked IS NOT DISTINCT FROM OLD.grade THEN
        -- Not the writer's choice: the speaker or the citations changed. A filled-in speaker decides afresh;
        -- a decided row keeps a grade a writer lowered.
        asked := CASE WHEN coalesce(OLD.speaker, 'unknown') = 'unknown' THEN NULL ELSE OLD.grade END;
    ELSIF TG_OP = 'UPDATE' AND OLD.grade = 'external' THEN
        asked := 'external';
    END IF;
    NEW.grade := CASE WHEN asked IN ('owner', 'agent', 'external')
                      THEN memory.lowest_grade(ARRAY[asked, rule]) ELSE rule END;
    NEW.source_id := coalesce(NEW.source_id, memory.default_source_id(NEW.source, NEW.uid, NEW.native));
    RETURN NEW;
END $$;

-- Backfill before the trigger exists, by the same rule (a heart row that cites only external items is kept and
-- graded external: it was written before the rule, and dropping it here would lose it).
UPDATE memory.messages SET grade = CASE
        WHEN role = 'user' AND (native->>'_compressed_summary') = 'true' THEN 'external'
        WHEN source = 'heart' THEN 'agent'
        ELSE memory.grade_of_speaker(speaker) END,
    source_id = coalesce(source_id, memory.default_source_id(source, uid, native))
WHERE grade IS NULL;
UPDATE memory.messages AS d SET grade = (
        SELECT memory.lowest_grade(array_agg(coalesce(s.grade, 'external')))
        FROM unnest(memory.cited_uids(d.native)) AS e(uid) LEFT JOIN memory.messages AS s ON s.uid = e.uid)
WHERE d.source = 'heart' AND cardinality(memory.cited_uids(d.native)) > 0
  AND NOT EXISTS (SELECT 1 FROM pg_trigger
                  WHERE tgrelid = 'memory.messages'::regclass AND tgname = 'messages_trust_grade');

-- Named after messages_speaker_by_rule so it fires after it (BEFORE triggers run in name order): the speaker is
-- always decided when the grade is.
CREATE OR REPLACE TRIGGER messages_trust_grade BEFORE INSERT OR UPDATE OF speaker, grade, native, source_id
    ON memory.messages FOR EACH ROW EXECUTE FUNCTION memory.messages_trust_grade();

ALTER TABLE memory.messages ALTER COLUMN grade SET NOT NULL;
ALTER TABLE memory.messages ALTER COLUMN source_id SET NOT NULL;
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint
                   WHERE conrelid = 'memory.messages'::regclass AND conname = 'messages_grade_kind') THEN
        ALTER TABLE memory.messages ADD CONSTRAINT messages_grade_kind CHECK (grade IN ('owner', 'agent', 'external'));
    END IF;
END $$;

-- A gateway note is the gateway's (agent), unless it quotes or reads out someone: then the lower of the two.
ALTER TABLE memory.message_parts ADD COLUMN IF NOT EXISTS grade text GENERATED ALWAYS AS (
    CASE WHEN quoted_speaker IS NULL THEN memory.grade_of_speaker(speaker)
         ELSE memory.lowest_grade(ARRAY[memory.grade_of_speaker(speaker), memory.grade_of_speaker(quoted_speaker)])
    END) STORED;

-- An LCM-X summary's grade is the lowest grade of every message under it, through every condensed level, and its
-- source ids are those messages (msg:<uid>); a source message the store does not hold is external.
CREATE OR REPLACE VIEW ctx.summary_grades WITH (security_invoker = true) AS
WITH RECURSIVE reach (agent_id, node_id, source_type, source_id) AS (
    SELECT n.agent_id, n.node_id, n.source_type, s.value::bigint
    FROM ctx.summary_nodes AS n, jsonb_array_elements_text(n.source_ids::jsonb) AS s
    UNION
    SELECT r.agent_id, r.node_id, c.source_type, s.value::bigint
    FROM reach AS r
    JOIN ctx.summary_nodes AS c ON r.source_type = 'nodes' AND c.agent_id = r.agent_id AND c.node_id = r.source_id
    CROSS JOIN LATERAL jsonb_array_elements_text(c.source_ids::jsonb) AS s
)
SELECT r.agent_id, r.node_id, memory.lowest_grade(array_agg(coalesce(m.grade, 'external'))) AS grade,
       array_agg(DISTINCT 'msg:' || m.uid ORDER BY 'msg:' || m.uid) FILTER (WHERE m.uid IS NOT NULL) AS source_ids
FROM reach AS r
LEFT JOIN ctx.lcm_messages AS p ON p.agent_id = r.agent_id AND p.store_id = r.source_id
LEFT JOIN memory.messages AS m ON m.uid = p.text_uid
WHERE r.source_type = 'messages'
GROUP BY r.agent_id, r.node_id;
GRANT SELECT ON ctx.summary_grades TO lina_app;

-- The brain's timeline and atoms carry the same three grades (0014); their writers (hermes_cli/brain) set them.
