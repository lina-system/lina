WITH searchable_messages AS (
    SELECT m.message_id, m.uid, m.source, m.role, m.author, m.speaker, m.speaker_ref,
           m.ts, m.observed_at, m.forgotten_at,
           CASE WHEN s.message_id IS NULL THEN m.content ELSE s.instruction END AS content,
           CASE WHEN s.message_id IS NULL THEN m.tsv ELSE to_tsvector('simple', s.instruction) END AS tsv
    FROM memory.messages m
    LEFT JOIN unnest($1::bigint[], $2::text[]) AS s(message_id, instruction) USING (message_id)
    -- The caller supplies the host's visibility policy for mail and Slack.
    WHERE NOT (
        m.source = 'messages' AND coalesce(
            nullif(m.native->>'platform', ''),
            CASE WHEN split_part(m.conversation_id, ':', 1) = 'messages'
                 THEN nullif(split_part(m.conversation_id, ':', 2), '') END,
            split_part(m.author, ':', 1),
            ''
        ) IN (
            SELECT kind FROM unnest($10::text[]) AS disabled(kind)
            WHERE kind IN ('mail', 'slack')
        )
    )
)
SELECT uid, content
FROM (
    SELECT DISTINCT ON (split_part(uid, '#', 1)) uid, content, coalesce(ts, observed_at) AS at,
           (ts_rank(tsv, plainto_tsquery('simple', $3), 1)
            + (SELECT count(*) FROM unnest($4::text[]) AS p(pattern) WHERE content ILIKE p.pattern)::float
              / cardinality($4) / (1 + ln(1 + length(content) / 2000::float)))
           * greatest(0.5, least(1.0, power(0.5, extract(epoch FROM now() - coalesce(ts, observed_at)) / 1209600::float8)))
           AS score
    FROM searchable_messages
    WHERE forgotten_at IS NULL AND content IS NOT NULL AND source = ANY($5::text[])
      AND (source <> 'agent' OR (role IN ('user', 'assistant') AND content !~ $6))
      AND (tsv @@ plainto_tsquery('simple', $3) OR content ILIKE ANY($4))
      AND ($7::text[] IS NULL OR author = ANY($7) OR speaker_ref = ANY($7))
      AND ($8::text[] IS NULL OR speaker = ANY($8))
    ORDER BY split_part(uid, '#', 1), message_id DESC
) hits
ORDER BY score DESC, at DESC
LIMIT $9
