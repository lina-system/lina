-- ctx: the LCM-X context engine's tables on Postgres (lina#16). Plan: docs/lcm-x.md "On the store".
-- LCM-X runs its own SQLite SQL against these through plugins/context_engine/lcm_x_store.py, which translates the
-- dialect; the tables keep LCM-X's names and columns so its SQL reaches them unchanged. Every table has agent_id
-- (filled from lina.agent_id), so 10-lina-store.sh's event trigger gives it row-level security, and its keys lead
-- with agent_id. LCM's `messages` holds no text here: ctx.lcm_messages places a message, its text is the
-- memory.messages row text_uid names, and the view ctx.messages joins the two under LCM's column names.

-- SQLite functions LCM's SQL calls that Postgres lacks or spells differently. pg_catalog comes first in every
-- search_path, so these never shadow a built-in of the same signature (two-argument max/min have none).
CREATE OR REPLACE FUNCTION ctx.json_extract(doc text, path text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT jsonb_path_query_first(doc::jsonb, path::jsonpath) #>> '{}' $$;
CREATE OR REPLACE FUNCTION ctx.json_type(doc text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE jsonb_typeof(doc::jsonb)
        WHEN 'string' THEN 'text'
        WHEN 'boolean' THEN (doc::jsonb)::text
        WHEN 'number' THEN CASE WHEN (doc::jsonb)::text ~ '^-?[0-9]+$' THEN 'integer' ELSE 'real' END
        ELSE jsonb_typeof(doc::jsonb) END $$;
CREATE OR REPLACE FUNCTION ctx.json_type(doc text, path text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT ctx.json_type((jsonb_path_query_first(doc::jsonb, path::jsonpath))::text) $$;
CREATE OR REPLACE FUNCTION ctx.json_array_length(doc text) RETURNS bigint
    LANGUAGE sql IMMUTABLE AS $$ SELECT jsonb_array_length(doc::jsonb)::bigint $$;
CREATE OR REPLACE FUNCTION ctx.json_valid(doc text) RETURNS boolean
    LANGUAGE plpgsql IMMUTABLE AS $$
BEGIN
    PERFORM doc::jsonb;
    RETURN doc IS NOT NULL;
EXCEPTION WHEN others THEN
    RETURN false;
END $$;
-- json_each(doc): SQLite's columns for the top level of an array or object (key, value as text, type, atom).
CREATE OR REPLACE FUNCTION ctx.json_each(doc text)
    RETURNS TABLE (key text, value text, type text, atom text, id bigint, parent bigint, fullkey text, path text)
    LANGUAGE sql IMMUTABLE AS $$
    SELECT (e.ord - 1)::text, e.v #>> '{}', ctx.json_type(e.v::text), CASE WHEN jsonb_typeof(e.v) IN ('object', 'array')
           THEN NULL ELSE e.v #>> '{}' END, e.ord, NULL::bigint, '$[' || (e.ord - 1) || ']', '$'
      FROM jsonb_array_elements(CASE WHEN jsonb_typeof(doc::jsonb) = 'array' THEN doc::jsonb END)
           WITH ORDINALITY AS e(v, ord)
    UNION ALL
    SELECT o.k, o.v #>> '{}', ctx.json_type(o.v::text), CASE WHEN jsonb_typeof(o.v) IN ('object', 'array')
           THEN NULL ELSE o.v #>> '{}' END, o.ord, NULL::bigint, '$.' || o.k, '$'
      FROM jsonb_each(CASE WHEN jsonb_typeof(doc::jsonb) = 'object' THEN doc::jsonb END) WITH ORDINALITY AS o(k, v, ord) $$;
CREATE OR REPLACE FUNCTION ctx.json(doc text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT (doc::jsonb)::text $$;
CREATE OR REPLACE FUNCTION ctx.json_quote(value text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT to_jsonb(value)::text $$;
CREATE OR REPLACE FUNCTION ctx.json_array(VARIADIC items text[] DEFAULT '{}') RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(jsonb_agg(i), '[]'::jsonb)::text FROM unnest(items) AS i $$;
CREATE OR REPLACE FUNCTION ctx.json_array(VARIADIC items bigint[]) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(jsonb_agg(i), '[]'::jsonb)::text FROM unnest(items) AS i $$;
CREATE OR REPLACE FUNCTION ctx.json_object(VARIADIC kv text[]) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT jsonb_object(kv)::text $$;
CREATE OR REPLACE FUNCTION ctx.json_set(doc text, path text, value text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_set(doc::jsonb, string_to_array(ltrim(ltrim(path, '$'), '.'), '.'), to_jsonb(value), true)::text $$;
-- A number set with json_set stays a JSON number, as SQLite writes it.
CREATE OR REPLACE FUNCTION ctx.json_set(doc text, path text, value bigint) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_set(doc::jsonb, string_to_array(ltrim(ltrim(path, '$'), '.'), '.'), to_jsonb(value), true)::text $$;
CREATE OR REPLACE FUNCTION ctx.json_set(doc text, path text, value numeric) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_set(doc::jsonb, string_to_array(ltrim(ltrim(path, '$'), '.'), '.'), to_jsonb(value), true)::text $$;
CREATE OR REPLACE FUNCTION ctx.json_set(doc text, path text, value double precision) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT jsonb_set(doc::jsonb, string_to_array(ltrim(ltrim(path, '$'), '.'), '.'), to_jsonb(value), true)::text $$;
-- SQLite's comparisons are the integers 0 and 1, so LCM-X sums them (SUM(expires_at < ?)): the same over booleans.
CREATE OR REPLACE FUNCTION ctx.lcm_sum_bool_step(state bigint, value boolean) RETURNS bigint
    LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN value IS NULL THEN state ELSE coalesce(state, 0) + value::int END $$;
CREATE OR REPLACE AGGREGATE ctx.sum(boolean) (SFUNC = ctx.lcm_sum_bool_step, STYPE = bigint);
CREATE OR REPLACE FUNCTION ctx.json_remove(doc text, path text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT (doc::jsonb #- string_to_array(ltrim(ltrim(path, '$'), '.'), '.'))::text $$;
CREATE OR REPLACE FUNCTION ctx.json_group_array_step(state jsonb, value anyelement) RETURNS jsonb
    LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(state, '[]'::jsonb) || jsonb_build_array(value) $$;
CREATE OR REPLACE FUNCTION ctx.json_group_array_final(state jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(state, '[]'::jsonb)::text $$;
CREATE OR REPLACE AGGREGATE ctx.json_group_array(anyelement) (
    SFUNC = ctx.json_group_array_step, STYPE = jsonb, FINALFUNC = ctx.json_group_array_final);
CREATE OR REPLACE FUNCTION ctx.ifnull(a anycompatible, b anycompatible) RETURNS anycompatible
    LANGUAGE sql IMMUTABLE AS $$ SELECT coalesce(a, b) $$;
-- LCM-X registers lcm_summary_has_text(summary) as a Python function (str.strip()'s Unicode whitespace); the store
-- runs no Python, so the same test is SQL here and the connection's create_function skips it.
CREATE OR REPLACE FUNCTION ctx.lcm_summary_has_text(value text) RETURNS integer
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN value IS NULL THEN 0
                WHEN value ~ '^[\s\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000\u001c-\u001f]*$' THEN 0
                ELSE 1 END $$;
CREATE OR REPLACE FUNCTION ctx.instr(haystack text, needle text) RETURNS integer
    LANGUAGE sql IMMUTABLE AS $$ SELECT strpos(haystack, needle) $$;
CREATE OR REPLACE FUNCTION ctx.substr(value text, start bigint, len bigint) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN start < 0 THEN substr(value, greatest(length(value) + start::int + 1, 1), len::int)
                ELSE substr(value, greatest(start, 1)::int, (len - CASE WHEN start < 1 THEN 1 - start ELSE 0 END)::int) END $$;
CREATE OR REPLACE FUNCTION ctx.substr(value text, start bigint) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN start < 0 THEN substr(value, greatest(length(value) + start::int + 1, 1))
                ELSE substr(value, greatest(start, 1)::int) END $$;
CREATE OR REPLACE FUNCTION ctx.max(a anycompatible, b anycompatible) RETURNS anycompatible
    LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN a IS NULL OR b IS NULL THEN NULL ELSE greatest(a, b) END $$;
CREATE OR REPLACE FUNCTION ctx.min(a anycompatible, b anycompatible) RETURNS anycompatible
    LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN a IS NULL OR b IS NULL THEN NULL ELSE least(a, b) END $$;
CREATE OR REPLACE FUNCTION ctx.max(a anycompatible, b anycompatible, c anycompatible) RETURNS anycompatible
    LANGUAGE sql IMMUTABLE AS $$ SELECT ctx.max(ctx.max(a, b), c) $$;
CREATE OR REPLACE FUNCTION ctx.min(a anycompatible, b anycompatible, c anycompatible) RETURNS anycompatible
    LANGUAGE sql IMMUTABLE AS $$ SELECT ctx.min(ctx.min(a, b), c) $$;
CREATE OR REPLACE FUNCTION ctx.hex(value bytea) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT upper(encode(value, 'hex')) $$;
CREATE OR REPLACE FUNCTION ctx.hex(value text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT upper(encode(convert_to(value, 'UTF8'), 'hex')) $$;
CREATE OR REPLACE FUNCTION ctx.char(VARIADIC codes integer[]) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT string_agg(chr(c), '' ORDER BY o) FROM unnest(codes) WITH ORDINALITY AS u(c, o) $$;
CREATE OR REPLACE FUNCTION ctx.unixepoch() RETURNS bigint
    LANGUAGE sql STABLE AS $$ SELECT floor(extract(epoch FROM now()))::bigint $$;
CREATE OR REPLACE FUNCTION ctx.julianday(value text) RETURNS double precision
    LANGUAGE sql STABLE AS $$
    SELECT extract(epoch FROM CASE WHEN value = 'now' THEN now() ELSE value::timestamptz END) / 86400.0 + 2440587.5 $$;
CREATE OR REPLACE FUNCTION ctx.typeof(value anyelement) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN value IS NULL THEN 'null'
                WHEN pg_typeof(value) IN ('bigint'::regtype, 'integer'::regtype, 'smallint'::regtype,
                                          'boolean'::regtype) THEN 'integer'
                WHEN pg_typeof(value) IN ('double precision'::regtype, 'real'::regtype, 'numeric'::regtype) THEN 'real'
                WHEN pg_typeof(value) = 'bytea'::regtype THEN 'blob'
                ELSE 'text' END $$;
CREATE OR REPLACE FUNCTION ctx.round(value double precision, digits integer) RETURNS double precision
    LANGUAGE sql IMMUTABLE AS $$ SELECT round(value::numeric, digits)::double precision $$;
CREATE OR REPLACE FUNCTION ctx.glob(pattern text, value text) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT value ~ ('^' || replace(replace(replace(regexp_replace(pattern, '([.+(){}|^$\\])', '\\\1', 'g'),
                                                       '*', '.*'), '?', '.'), '[^', '[^') || '$') $$;
-- strftime(format, time, modifiers...): the forms LCM uses: 'now' or epoch seconds with 'unixepoch', an ISO text.
CREATE OR REPLACE FUNCTION ctx.strftime(fmt text, value text, VARIADIC modifiers text[] DEFAULT '{}') RETURNS text
    LANGUAGE plpgsql STABLE AS $$
DECLARE
    t timestamptz;
    out text := fmt;
BEGIN
    IF value IS NULL THEN
        RETURN NULL;
    ELSIF value = 'now' THEN
        t := now();
    ELSIF 'unixepoch' = ANY (modifiers) OR value ~ '^-?[0-9]+(\.[0-9]+)?$' THEN
        t := to_timestamp(value::double precision);
    ELSE
        t := (value || CASE WHEN value ~ '(Z|[+-][0-9:]+)$' THEN '' ELSE '+00' END)::timestamptz;
    END IF;
    t := t AT TIME ZONE 'UTC';
    out := replace(out, '%s', floor(extract(epoch FROM t AT TIME ZONE 'UTC'))::bigint::text);
    out := replace(out, '%Y', to_char(t, 'YYYY'));
    out := replace(out, '%m', to_char(t, 'MM'));
    out := replace(out, '%d', to_char(t, 'DD'));
    out := replace(out, '%H', to_char(t, 'HH24'));
    out := replace(out, '%M', to_char(t, 'MI'));
    out := replace(out, '%f', to_char(t, 'SS.MS'));
    out := replace(out, '%S', to_char(t, 'SS'));
    out := replace(out, '%j', to_char(t, 'DDD'));
    out := replace(out, '%w', (extract(dow FROM t))::int::text);
    RETURN out;
END $$;
CREATE OR REPLACE FUNCTION ctx.datetime(value text, VARIADIC modifiers text[] DEFAULT '{}') RETURNS text
    LANGUAGE sql STABLE AS $$ SELECT ctx.strftime('%Y-%m-%d %H:%M:%S', value, VARIADIC modifiers) $$;
CREATE OR REPLACE FUNCTION ctx.group_concat_step(state text, value anyelement, sep text) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN value IS NULL THEN state WHEN state IS NULL THEN value::text ELSE state || sep || value::text END $$;
CREATE OR REPLACE FUNCTION ctx.group_concat_step(state text, value anyelement) RETURNS text
    LANGUAGE sql IMMUTABLE AS $$ SELECT ctx.group_concat_step(state, value, ',') $$;
CREATE OR REPLACE AGGREGATE ctx.group_concat(anyelement, text) (SFUNC = ctx.group_concat_step, STYPE = text);
CREATE OR REPLACE AGGREGATE ctx.group_concat(anyelement) (SFUNC = ctx.group_concat_step, STYPE = text);
CREATE OR REPLACE FUNCTION ctx.glob_match(value text, pattern text) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$ SELECT ctx.glob(pattern, value) $$;
CREATE OR REPLACE FUNCTION ctx.glob_no_match(value text, pattern text) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$ SELECT NOT ctx.glob(pattern, value) $$;
DO $$
BEGIN
    -- `x GLOB y` and `x NOT GLOB y`: the translator writes OPERATOR(ctx.~#) / OPERATOR(ctx.!~#).
    IF NOT EXISTS (SELECT 1 FROM pg_operator WHERE oprname = '~#' AND oprnamespace = 'ctx'::regnamespace) THEN
        CREATE OPERATOR ctx.~# (LEFTARG = text, RIGHTARG = text, FUNCTION = ctx.glob_match);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_operator WHERE oprname = '!~#' AND oprnamespace = 'ctx'::regnamespace) THEN
        CREATE OPERATOR ctx.!~# (LEFTARG = text, RIGHTARG = text, FUNCTION = ctx.glob_no_match);
    END IF;
END $$;
-- SQLite compares a number with text holding that number as equal (column affinity); LCM-X relies on it, e.g.
-- `node_id IN (SELECT value FROM json_each(source_ids))`. These operators give the same answer in ctx.
CREATE OR REPLACE FUNCTION ctx.number_matches_text(a double precision, b text) RETURNS boolean
    LANGUAGE sql IMMUTABLE AS $$
    SELECT CASE WHEN b ~ '^\s*-?[0-9]+(\.[0-9]*)?([eE][-+]?[0-9]+)?\s*$' THEN a = b::double precision ELSE false END $$;
DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['bigint', 'integer', 'double precision'] LOOP
        EXECUTE format('CREATE OR REPLACE FUNCTION ctx.num_eq_text(a %1$s, b text) RETURNS boolean LANGUAGE sql '
                       'IMMUTABLE AS $f$ SELECT ctx.number_matches_text(a::double precision, b) $f$', t);
        EXECUTE format('CREATE OR REPLACE FUNCTION ctx.num_ne_text(a %1$s, b text) RETURNS boolean LANGUAGE sql '
                       'IMMUTABLE AS $f$ SELECT NOT ctx.number_matches_text(a::double precision, b) $f$', t);
        EXECUTE format('CREATE OR REPLACE FUNCTION ctx.text_eq_num(a text, b %1$s) RETURNS boolean LANGUAGE sql '
                       'IMMUTABLE AS $f$ SELECT ctx.number_matches_text(b::double precision, a) $f$', t);
        EXECUTE format('CREATE OR REPLACE FUNCTION ctx.text_ne_num(a text, b %1$s) RETURNS boolean LANGUAGE sql '
                       'IMMUTABLE AS $f$ SELECT NOT ctx.number_matches_text(b::double precision, a) $f$', t);
        IF NOT EXISTS (SELECT 1 FROM pg_operator WHERE oprname = '=' AND oprnamespace = 'ctx'::regnamespace
                       AND oprleft = t::regtype AND oprright = 'text'::regtype) THEN
            EXECUTE format('CREATE OPERATOR ctx.= (LEFTARG = %s, RIGHTARG = text, FUNCTION = ctx.num_eq_text, '
                           'COMMUTATOR = OPERATOR(ctx.=), NEGATOR = OPERATOR(ctx.<>))', t);
            EXECUTE format('CREATE OPERATOR ctx.<> (LEFTARG = %s, RIGHTARG = text, FUNCTION = ctx.num_ne_text, '
                           'NEGATOR = OPERATOR(ctx.=))', t);
            EXECUTE format('CREATE OPERATOR ctx.= (LEFTARG = text, RIGHTARG = %s, FUNCTION = ctx.text_eq_num, '
                           'NEGATOR = OPERATOR(ctx.<>))', t);
            EXECUTE format('CREATE OPERATOR ctx.<> (LEFTARG = text, RIGHTARG = %s, FUNCTION = ctx.text_ne_num, '
                           'NEGATOR = OPERATOR(ctx.=))', t);
        END IF;
    END LOOP;
END $$;
-- Ordering a TEXT column against a number: SQLite gives the number the column's text affinity and compares text.
DO $$
DECLARE
    t text;
    op record;
BEGIN
    FOREACH t IN ARRAY ARRAY['bigint', 'integer', 'double precision'] LOOP
        FOR op IN SELECT * FROM (VALUES ('<', 'lt'), ('<=', 'le'), ('>', 'gt'), ('>=', 'ge')) AS v(sym, name) LOOP
            EXECUTE format('CREATE OR REPLACE FUNCTION ctx.text_%1$s_num(a text, b %2$s) RETURNS boolean LANGUAGE sql '
                           'IMMUTABLE AS $f$ SELECT a %3$s b::text $f$', op.name, t, op.sym);
            IF NOT EXISTS (SELECT 1 FROM pg_operator WHERE oprname = op.sym AND oprnamespace = 'ctx'::regnamespace
                           AND oprleft = 'text'::regtype AND oprright = t::regtype) THEN
                EXECUTE format('CREATE OPERATOR ctx.%1$s (LEFTARG = text, RIGHTARG = %2$s, FUNCTION = ctx.text_%3$s_num)',
                               op.sym, t, op.name);
            END IF;
        END LOOP;
    END LOOP;
END $$;
CREATE OR REPLACE FUNCTION ctx.lcm_raise(message text) RETURNS void
    LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '%', message USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE OR REPLACE FUNCTION ctx.lcm_now() RETURNS double precision
    LANGUAGE sql STABLE AS $$ SELECT floor(extract(epoch FROM now())) $$;

-- Tables, in LCM-X's column order (the store's SELECT * and positional reads depend on it).
CREATE TABLE IF NOT EXISTS ctx.lcm_messages (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    store_id bigint NOT NULL,
    session_id text NOT NULL,
    source text DEFAULT '',
    conversation_id text DEFAULT '',
    text_uid text NOT NULL,
    content_sha256 text NOT NULL,
    timestamp double precision NOT NULL,
    token_estimate bigint DEFAULT 0,
    pinned bigint DEFAULT 0,
    ingested_at double precision,
    observed_at double precision,
    observed_at_source text,
    sender_id text,
    sender_source text,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS (store_id) STORED,
    PRIMARY KEY (agent_id, store_id)
);

CREATE TABLE IF NOT EXISTS ctx.metadata (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    key text,
    value text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, key)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_migration_state (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    step_name text,
    completed_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, step_name)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_lifecycle_state (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    conversation_id text,
    current_session_id text,
    last_finalized_session_id text,
    current_frontier_store_id bigint NOT NULL DEFAULT 0,
    last_finalized_frontier_store_id bigint NOT NULL DEFAULT 0,
    debt_kind text,
    debt_size_estimate bigint NOT NULL DEFAULT 0,
    current_bound_at double precision,
    last_finalized_at double precision,
    debt_updated_at double precision,
    last_maintenance_attempt_at double precision,
    last_rollover_at double precision,
    last_reset_at double precision,
    updated_at double precision NOT NULL DEFAULT (floor(extract(epoch FROM now()))),
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, conversation_id)
);

CREATE TABLE IF NOT EXISTS ctx.summary_nodes (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    node_id bigint NOT NULL,
    session_id text NOT NULL,
    depth bigint NOT NULL DEFAULT 0,
    summary text NOT NULL,
    token_count bigint DEFAULT 0,
    source_token_count bigint DEFAULT 0,
    source_ids text NOT NULL DEFAULT '[]',
    source_type text NOT NULL DEFAULT 'messages',
    created_at double precision NOT NULL,
    earliest_at double precision,
    latest_at double precision,
    expand_hint text DEFAULT '',
    access_scope text,
    rowid bigint GENERATED ALWAYS AS (node_id) STORED,
    PRIMARY KEY (agent_id, node_id)
);

CREATE TABLE IF NOT EXISTS ctx.summary_node_provenance (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    node_id bigint,
    escalation_level bigint NOT NULL,
    model text NOT NULL DEFAULT '',
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS (node_id) STORED,
    PRIMARY KEY (agent_id, node_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_assertion_sources (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    source_store_id bigint NOT NULL,
    extraction_version text NOT NULL
                CHECK(length(trim(extraction_version)) BETWEEN 1 AND 128),
    source_content_sha256 text NOT NULL
                CHECK(length(source_content_sha256) = 64),
    source_session_id text NOT NULL,
    source_role text NOT NULL,
    source_name text NOT NULL DEFAULT '',
    source_timestamp double precision NOT NULL,
    candidate_digest text NOT NULL CHECK(length(candidate_digest) = 64),
    assertion_count bigint NOT NULL DEFAULT 0 CHECK(assertion_count >= 0),
    relation_count bigint NOT NULL DEFAULT 0 CHECK(relation_count >= 0),
    processed_at double precision NOT NULL DEFAULT (floor(extract(epoch FROM now()))),
    invalidated_at double precision,
    invalidation_reason text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, source_store_id, extraction_version, source_content_sha256),
    CHECK(
                (invalidated_at IS NULL AND invalidation_reason IS NULL)
                OR (invalidated_at IS NOT NULL AND length(trim(invalidation_reason)) > 0)
            )
);

CREATE TABLE IF NOT EXISTS ctx.lcm_assertions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    assertion_id text CHECK(length(assertion_id) = 64),
    source_store_id bigint NOT NULL,
    extraction_version text NOT NULL,
    source_content_sha256 text NOT NULL CHECK(length(source_content_sha256) = 64),
    subject_key text NOT NULL CHECK(length(trim(subject_key)) > 0),
    predicate_key text NOT NULL CHECK(length(trim(predicate_key)) > 0),
    object_json text NOT NULL,
    value_text text NOT NULL DEFAULT '',
    kind text NOT NULL CHECK(kind IN (
                'fact', 'event', 'preference', 'recommendation', 'commitment',
                'action', 'status', 'quotation'
            )),
    polarity text NOT NULL CHECK(polarity IN ('positive', 'negative', 'unknown')),
    strength double precision CHECK(strength IS NULL OR (strength >= 0.0 AND strength <= 1.0)),
    scope_key text NOT NULL DEFAULT '',
    speaker_role text NOT NULL DEFAULT '',
    observed_at double precision NOT NULL,
    event_at double precision,
    valid_from double precision,
    valid_to double precision,
    source_span_start bigint NOT NULL CHECK(source_span_start >= 0),
    source_span_end bigint NOT NULL CHECK(source_span_end > source_span_start),
    source_quote text NOT NULL,
    confidence double precision NOT NULL CHECK(confidence >= 0.0 AND confidence <= 1.0),
    created_at double precision NOT NULL DEFAULT (floor(extract(epoch FROM now()))),
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, assertion_id),
    CHECK(valid_from IS NULL OR valid_to IS NULL OR valid_to > valid_from)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_assertion_relations (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    relation_id text CHECK(length(relation_id) = 64),
    source_store_id bigint NOT NULL,
    extraction_version text NOT NULL,
    source_content_sha256 text NOT NULL CHECK(length(source_content_sha256) = 64),
    from_assertion_id text NOT NULL,
    relation_type text NOT NULL CHECK(relation_type IN (
                'confirms', 'supersedes', 'contradicts', 'narrows', 'weakens',
                'reverses', 'cancels', 'fulfills', 'quotes'
            )),
    to_assertion_id text NOT NULL,
    source_span_start bigint NOT NULL CHECK(source_span_start >= 0),
    source_span_end bigint NOT NULL CHECK(source_span_end > source_span_start),
    source_quote text NOT NULL,
    confidence double precision NOT NULL CHECK(confidence >= 0.0 AND confidence <= 1.0),
    created_at double precision NOT NULL DEFAULT (floor(extract(epoch FROM now()))),
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, relation_id),
    CHECK(from_assertion_id <> to_assertion_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_query_corpus_state (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    singleton bigint CHECK(singleton = 1),
    generation bigint NOT NULL DEFAULT 0 CHECK(generation >= 0),
    max_store_id bigint NOT NULL DEFAULT 0 CHECK(max_store_id >= 0),
    row_count bigint NOT NULL DEFAULT 0 CHECK(row_count >= 0),
    changed_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS (singleton) STORED,
    PRIMARY KEY (agent_id, singleton)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_query_corpus_events (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    generation bigint CHECK(generation > 0),
    store_id bigint NOT NULL CHECK(store_id > 0),
    mutation text NOT NULL CHECK(mutation IN ('insert', 'update', 'delete')),
    session_id text NOT NULL DEFAULT '',
    conversation_id text NOT NULL DEFAULT '',
    source text NOT NULL DEFAULT '',
    role text NOT NULL DEFAULT '',
    source_timestamp double precision,
    changed_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS (generation) STORED,
    PRIMARY KEY (agent_id, generation)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_query_views (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    view_id text CHECK(length(view_id) = 64),
    identity_json text NOT NULL,
    status text NOT NULL CHECK(status IN (
                'ready', 'stale', 'building', 'failed', 'expired'
            )),
    generation bigint NOT NULL DEFAULT 0 CHECK(generation >= 0),
    current_version bigint NOT NULL DEFAULT 0 CHECK(current_version >= 0),
    build_nonce text NOT NULL DEFAULT '',
    lease_expires_at double precision,
    stale_reason text NOT NULL DEFAULT '',
    hit_count bigint NOT NULL DEFAULT 0 CHECK(hit_count >= 0),
    promotion_status text NOT NULL DEFAULT 'probationary'
                CHECK(promotion_status IN ('probationary', 'promoted')),
    created_at double precision NOT NULL,
    updated_at double precision NOT NULL,
    expires_at double precision,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, view_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_query_view_versions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    view_id text NOT NULL,
    version bigint NOT NULL CHECK(version > 0),
    manifest_json text NOT NULL,
    trace_json text NOT NULL DEFAULT '',
    completeness text NOT NULL CHECK(completeness IN ('complete', 'partial')),
    search_policy_version text NOT NULL,
    corpus_generation bigint NOT NULL CHECK(corpus_generation >= 0),
    coverage_store_id bigint NOT NULL CHECK(coverage_store_id >= 0),
    published_at double precision NOT NULL,
    expires_at double precision,
    supersedes_version bigint,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, view_id, version)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_query_view_sources (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    view_id text NOT NULL,
    version bigint NOT NULL,
    source_store_id bigint NOT NULL CHECK(source_store_id > 0),
    source_content_sha256 text NOT NULL CHECK(length(source_content_sha256) = 64),
    source_session_id text NOT NULL,
    source_conversation_id text NOT NULL DEFAULT '',
    source_name text NOT NULL DEFAULT '',
    source_role text NOT NULL,
    source_timestamp double precision NOT NULL,
    span_start bigint NOT NULL CHECK(span_start >= 0),
    span_end bigint NOT NULL CHECK(span_end > span_start),
    quote text NOT NULL,
    assertion_id text NOT NULL DEFAULT '',
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, view_id, version, source_store_id, span_start, span_end)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_embedding_profile (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    identity_hash text,
    provider text NOT NULL,
    model_name text NOT NULL,
    revision text NOT NULL DEFAULT '',
    dim bigint CHECK(dim BETWEEN 1 AND 4096),
    dtype text NOT NULL DEFAULT 'float32',
    byteorder text NOT NULL DEFAULT 'little',
    task text NOT NULL DEFAULT 'summary',
    registered_at text,
    active bigint DEFAULT 1,
    archived_at text NULL,
    data_version bigint NOT NULL DEFAULT 0,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, identity_hash)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_embedding_meta (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    embedded_id text,
    embedded_kind text CHECK(embedded_kind IN ('summary')),
    identity_hash text,
    embedded_at text,
    source_token_count bigint,
    archived bigint DEFAULT 0,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, embedded_id, embedded_kind, identity_hash)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_embedding_backfill_inflight (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    embedded_id text,
    identity_hash text,
    lease_id text,
    generation bigint,
    claimed_at double precision,
    state text NOT NULL DEFAULT 'claimed' CHECK(state IN ('claimed', 'dispatched', 'uncertain')),
    request_id text,
    updated_at double precision,
    last_error text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, embedded_id, identity_hash)
);

-- scripts/import_lossless_claw.py's record of imported messages.
CREATE TABLE IF NOT EXISTS ctx.lcm_imported_messages (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    import_id text NOT NULL,
    source_message_id bigint NOT NULL,
    source_message_key text,
    source_conversation_id bigint NOT NULL,
    source_session text NOT NULL,
    target_store_id bigint NOT NULL,
    imported_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, import_id, source_message_id)
);
CREATE INDEX IF NOT EXISTS idx_lcm_imported_messages_target ON ctx.lcm_imported_messages (agent_id, target_store_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lcm_imported_messages_key
    ON ctx.lcm_imported_messages (agent_id, import_id, source_message_key) WHERE source_message_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS ctx.lcm_imported_summaries (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    import_id text NOT NULL,
    source_summary_id text NOT NULL,
    source_conversation_id bigint NOT NULL,
    source_session text NOT NULL,
    target_node_id bigint NOT NULL,
    imported_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, import_id, source_summary_id)
);
CREATE INDEX IF NOT EXISTS idx_lcm_imported_summaries_target ON ctx.lcm_imported_summaries (agent_id, target_node_id);

-- lcm_embedding_vectors is a view over vec.embeddings (0008).

CREATE TABLE IF NOT EXISTS ctx.lcm_embedding_binary (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    embedded_id text,
    identity_hash text,
    bits bytea NOT NULL,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, embedded_id, identity_hash)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_rollups (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    rollup_id bigint NOT NULL,
    period_kind text NOT NULL CHECK (period_kind IN ('day', 'week', 'month')),
    period_start text NOT NULL,
    scope text NOT NULL,
    summary text,
    token_count bigint,
    status text NOT NULL DEFAULT 'building'
                CHECK (status IN ('building', 'ready', 'stale', 'failed')),
    built_at text,
    source_fingerprint text,
    error text,
    generation bigint NOT NULL DEFAULT 0,
    lease_expires_at text,
    lease_nonce text NOT NULL DEFAULT '',
    failed_at text,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS (rollup_id) STORED,
    PRIMARY KEY (agent_id, rollup_id),
    UNIQUE (agent_id, period_kind, period_start, scope)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_rollup_sources (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    rollup_id bigint NOT NULL,
    node_id bigint NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, rollup_id, node_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_rollup_invalidations (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    event_id bigint NOT NULL,
    node_id bigint,
    scope text NOT NULL,
    covered_start double precision NOT NULL,
    covered_end double precision NOT NULL,
    next_day text,
    operation text NOT NULL CHECK(operation IN ('insert', 'delete', 'update')),
    created_at double precision NOT NULL DEFAULT (floor(extract(epoch FROM now()))),
    access_scope text,
    rowid bigint GENERATED ALWAYS AS (event_id) STORED,
    PRIMARY KEY (agent_id, event_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_rollup_state (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    period_kind text NOT NULL,
    scope text NOT NULL DEFAULT '',
    last_build_cursor text,
    last_built_at text,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, period_kind, scope)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_chunk_meta (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    chunk_id text,
    identity_hash text,
    store_id bigint,
    chunk_index bigint,
    char_start bigint,
    char_end bigint,
    token_estimate bigint,
    embedded_at text,
    archived bigint DEFAULT 0,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, chunk_id, identity_hash)
);

-- lcm_chunk_vectors is a view over vec.chunks and vec.embeddings (0008).

CREATE TABLE IF NOT EXISTS ctx.lcm_chunk_binary (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    chunk_id text,
    identity_hash text,
    bits bytea NOT NULL,
    access_scope text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, chunk_id, identity_hash)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_entities (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    entity_id text CHECK(length(entity_id)=36 AND substr(entity_id,1,4)='ent_' AND substr(entity_id,5) !~ '[^0-9a-f]'),
    scope_key text NOT NULL,
    kind text NOT NULL CHECK(kind IN ('person','agent','org','project','place','thing','topic','other')),
    display_name text NOT NULL,
    status text NOT NULL CHECK(status IN ('active','merged','forgotten')),
    merged_into text,
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, entity_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_entity_aliases (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    alias_id bigint NOT NULL,
    entity_id text NOT NULL,
    scope_key text NOT NULL,
    alias_norm text NOT NULL,
    alias_display text NOT NULL,
    alias_kind text NOT NULL CHECK(alias_kind IN ('name','nickname','relation_phrase','platform_id')),
    owner_entity_id text,
    source_store_ids text NOT NULL CHECK(ctx.json_type(source_store_ids)='array'),
    created_at double precision NOT NULL,
    retired_at double precision,
    rowid bigint GENERATED ALWAYS AS (alias_id) STORED,
    PRIMARY KEY (agent_id, alias_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_entity_keys (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    key_id bigint NOT NULL,
    subject_key text NOT NULL,
    owner_entity_id text,
    entity_id text NOT NULL,
    method text NOT NULL CHECK(method IN ('alias_match','override')),
    source_store_ids text NOT NULL CHECK(ctx.json_type(source_store_ids)='array'),
    scope_key text NOT NULL,
    created_at double precision NOT NULL,
    retired_at double precision,
    rowid bigint GENERATED ALWAYS AS (key_id) STORED,
    PRIMARY KEY (agent_id, key_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_memory_notes (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    note_id text CHECK(length(note_id)=37 AND substr(note_id,1,5)='note_' AND substr(note_id,6) !~ '[^0-9a-f]'),
    scope_key text NOT NULL,
    entity_id text,
    about_entity_id text,
    kind text NOT NULL CHECK(kind IN ('relationship','preference','fact','context','instruction','retraction','history')),
    text text NOT NULL,
    asserted_by_entity_id text,
    cites text NOT NULL CHECK(ctx.json_type(cites)='array'),
    cites_file_revisions text NOT NULL CHECK(ctx.json_type(cites_file_revisions)='array'),
    author text NOT NULL CHECK(author IN ('agent','consolidator','user','host_memory_file','detector')),
    superseded_by text,
    retracts_note_id text,
    retracts_store_ids text NOT NULL CHECK(ctx.json_type(retracts_store_ids)='array'),
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, note_id),
    CHECK(ctx.json_array_length(cites)>0 OR ctx.json_array_length(cites_file_revisions)>0)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_memory_events (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    event_id bigint NOT NULL,
    scope_key text NOT NULL,
    op text NOT NULL CHECK(op IN ('create_entity','add_alias','retire_alias','change_kind','merge','split','add_note','supersede_note','forget','override_key','undo','host_file_revision','restore_alias','set_entity_status')),
    payload_json text NOT NULL,
    actor text NOT NULL,
    created_at double precision NOT NULL,
    target_event_id bigint CHECK(target_event_id IS NULL OR op='undo'),
    rowid bigint GENERATED ALWAYS AS (event_id) STORED,
    PRIMARY KEY (agent_id, event_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_memory_host_file_revisions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    revision_id text CHECK(length(revision_id)=36 AND substr(revision_id,1,4)='rev_' AND substr(revision_id,5) !~ '[^0-9a-f]'),
    scope_key text NOT NULL,
    path text NOT NULL,
    revision_sha text NOT NULL,
    content_hash text NOT NULL,
    captured_at double precision NOT NULL,
    profile_key text NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, revision_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_tenants (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    tenant_id text,
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, tenant_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_principals (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    principal_id text,
    tenant_id text NOT NULL,
    status text NOT NULL CHECK(status IN ('active', 'suspended')),
    created_at double precision NOT NULL,
    updated_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, principal_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_collections (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    collection_id text,
    tenant_id text NOT NULL,
    kind text NOT NULL CHECK(kind IN ('own', 'shared')),
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, collection_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_memberships (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    principal_id text NOT NULL,
    collection_id text NOT NULL,
    grants text NOT NULL,
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, principal_id, collection_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_revisions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    tenant_id text,
    policy_revision bigint NOT NULL DEFAULT 0,
    membership_revision bigint NOT NULL DEFAULT 0,
    revocation_epoch bigint NOT NULL DEFAULT 0,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, tenant_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_audit (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    event_id bigint NOT NULL,
    occurred_at double precision NOT NULL,
    tenant_id text,
    principal_id text,
    operation text NOT NULL,
    allowed bigint NOT NULL CHECK(allowed IN (0, 1)),
    denial_reason text,
    detail text,
    rowid bigint GENERATED ALWAYS AS (event_id) STORED,
    PRIMARY KEY (agent_id, event_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_teams_requests (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    request_id text NOT NULL,
    payload_digest text NOT NULL,
    capability text NOT NULL,
    tenant_id text NOT NULL DEFAULT '',
    principal_id text,
    recorded_at double precision NOT NULL,
    result_json text,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, tenant_id, request_id)
);

CREATE TABLE IF NOT EXISTS ctx.message_relations (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    relation_id bigint NOT NULL,
    store_id bigint NOT NULL,
    kind text NOT NULL,
    related_store_id bigint,
    ordinal bigint,
    observed_at double precision,
    created_at double precision,
    rowid bigint GENERATED ALWAYS AS (relation_id) STORED,
    PRIMARY KEY (agent_id, relation_id)
);

CREATE TABLE IF NOT EXISTS ctx.host_uid_bindings (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    store_id bigint NOT NULL,
    uid text NOT NULL,
    lineage_key text NOT NULL,
    kind text NOT NULL,
    binding_version bigint NOT NULL,
    proof_kind text,
    created_at double precision,
    first_check text,
    disagree_seen bigint NOT NULL DEFAULT 0,
    rowid bigint GENERATED ALWAYS AS IDENTITY
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_corpora (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    singleton bigint CHECK(singleton = 1),
    identity_digest text NOT NULL,
    identity_json text NOT NULL,
    schema_version bigint NOT NULL,
    corpus_uid text,
    haystack_digest text,
    source_manifest_digest text,
    trajectory_count bigint,
    ingest_cursor bigint NOT NULL DEFAULT 0 CHECK(ingest_cursor >= 0),
    status text NOT NULL CHECK(status IN ('building', 'complete', 'invalid')),
    created_at double precision NOT NULL,
    completed_at double precision,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, singleton),
    UNIQUE (agent_id, identity_digest)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_sources (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    source_id bigint NOT NULL,
    trajectory_id text NOT NULL,
    ordinal bigint NOT NULL,
    source_json text NOT NULL,
    source_sha256 text NOT NULL,
    goal text NOT NULL,
    start_url text NOT NULL,
    outcome text,
    state_count bigint NOT NULL CHECK(state_count > 0),
    inserted_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS (source_id) STORED,
    PRIMARY KEY (agent_id, source_id),
    UNIQUE (agent_id, trajectory_id),
    UNIQUE (agent_id, ordinal)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_states (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    state_id bigint NOT NULL,
    source_id bigint NOT NULL,
    state_index bigint NOT NULL CHECK(state_index >= 0),
    sequence_ordinal bigint NOT NULL CHECK(sequence_ordinal >= 0),
    step bigint NOT NULL CHECK(step >= 0),
    url text NOT NULL,
    incoming_action text,
    thoughts text,
    text text NOT NULL,
    search_text text NOT NULL,
    observed_at double precision,
    observed_at_source text,
    occurred_at double precision,
    occurred_at_source text,
    ingested_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS (state_id) STORED,
    PRIMARY KEY (agent_id, state_id),
    UNIQUE (agent_id, source_id, state_index),
    UNIQUE (agent_id, source_id, sequence_ordinal),
    FOREIGN KEY (agent_id, source_id) REFERENCES ctx.lcm_trajectory_sources (agent_id, source_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_assets (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    asset_id bigint NOT NULL,
    state_id bigint NOT NULL,
    relative_path text NOT NULL,
    sha256 text NOT NULL,
    byte_size bigint NOT NULL CHECK(byte_size >= 0),
    rowid bigint GENERATED ALWAYS AS (asset_id) STORED,
    PRIMARY KEY (agent_id, asset_id),
    UNIQUE (agent_id, state_id),
    FOREIGN KEY (agent_id, state_id) REFERENCES ctx.lcm_trajectory_states (agent_id, state_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_ingest_receipts (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    ordinal bigint CHECK(ordinal >= 0),
    trajectory_id text NOT NULL,
    source_sha256 text NOT NULL,
    committed_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, ordinal),
    UNIQUE (agent_id, trajectory_id)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_transitions (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    transition_id bigint NOT NULL,
    source_id bigint NOT NULL,
    sequence_ordinal bigint NOT NULL CHECK(sequence_ordinal >= 1),
    pre_state_id bigint NOT NULL,
    post_state_id bigint NOT NULL,
    incoming_action text,
    rowid bigint GENERATED ALWAYS AS (transition_id) STORED,
    PRIMARY KEY (agent_id, transition_id),
    UNIQUE (agent_id, source_id, sequence_ordinal),
    UNIQUE (agent_id, source_id, post_state_id),
    FOREIGN KEY (agent_id, source_id) REFERENCES ctx.lcm_trajectory_sources (agent_id, source_id) ON DELETE CASCADE,
    FOREIGN KEY (agent_id, pre_state_id) REFERENCES ctx.lcm_trajectory_states (agent_id, state_id) ON DELETE CASCADE,
    FOREIGN KEY (agent_id, post_state_id) REFERENCES ctx.lcm_trajectory_states (agent_id, state_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_embedding_profiles (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    profile_digest text,
    provider text NOT NULL,
    model_name text NOT NULL,
    dim bigint NOT NULL CHECK(dim > 0),
    document_version text NOT NULL,
    source_manifest_digest text NOT NULL,
    document_count bigint NOT NULL CHECK(document_count >= 0),
    index_digest text NOT NULL,
    active bigint NOT NULL DEFAULT 0 CHECK(active IN (0, 1)),
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, profile_digest)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_embeddings (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    source_id bigint,
    profile_digest text NOT NULL,
    document_sha256 text NOT NULL,
    vector bytea NOT NULL,
    embedded_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, source_id),
    FOREIGN KEY (agent_id, profile_digest)
        REFERENCES ctx.lcm_trajectory_embedding_profiles (agent_id, profile_digest) ON DELETE CASCADE,
    FOREIGN KEY (agent_id, source_id) REFERENCES ctx.lcm_trajectory_sources (agent_id, source_id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_state_embedding_profiles (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    profile_digest text,
    provider text NOT NULL,
    model_name text NOT NULL,
    dim bigint NOT NULL CHECK(dim > 0),
    document_version text NOT NULL,
    source_manifest_digest text NOT NULL,
    state_count bigint NOT NULL CHECK(state_count >= 0),
    active bigint NOT NULL DEFAULT 0 CHECK(active IN (0, 1)),
    created_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, profile_digest)
);

CREATE TABLE IF NOT EXISTS ctx.lcm_trajectory_state_embeddings (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    state_id bigint,
    profile_digest text NOT NULL,
    document_sha256 text NOT NULL,
    vector bytea NOT NULL,
    embedded_at double precision NOT NULL,
    rowid bigint GENERATED ALWAYS AS IDENTITY,
    PRIMARY KEY (agent_id, state_id),
    FOREIGN KEY (agent_id, profile_digest)
        REFERENCES ctx.lcm_trajectory_state_embedding_profiles (agent_id, profile_digest) ON DELETE CASCADE,
    FOREIGN KEY (agent_id, state_id) REFERENCES ctx.lcm_trajectory_states (agent_id, state_id) ON DELETE CASCADE
);


-- Ids LCM-X lets SQLite allocate (INTEGER PRIMARY KEY AUTOINCREMENT) count per agent, as they counted per lcm.db:
-- the next id is one past the highest this agent's table has handed out or been given.
CREATE TABLE IF NOT EXISTS ctx.lcm_id_sequences (
    agent_id text NOT NULL DEFAULT public.lina_current_agent(),
    table_name text NOT NULL,
    last_id bigint NOT NULL,
    PRIMARY KEY (agent_id, table_name)
);

CREATE OR REPLACE FUNCTION ctx.lcm_assign_id() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    col text := TG_ARGV[0];
    given bigint := (to_jsonb(NEW) ->> TG_ARGV[0])::bigint;
    top bigint;
    retired bigint;
BEGIN
    -- SQLite's AUTOINCREMENT: the next id is one past the largest this agent's table ever held. The largest it holds
    -- is read from the (agent_id, id) key; the largest it deleted is kept in lcm_id_sequences. Nothing is written per
    -- insert (a counter row updated per insert slows a long transaction quadratically). One writer per agent at a
    -- time holds the store's write lock (lcm_x_store), so two inserts never read the same largest id.
    IF given IS NULL THEN
        EXECUTE format('SELECT max(%I) FROM %I.%I WHERE agent_id = $1', col, TG_TABLE_SCHEMA, TG_TABLE_NAME)
            INTO top USING NEW.agent_id;
        SELECT s.last_id INTO retired FROM ctx.lcm_id_sequences AS s
         WHERE s.agent_id = NEW.agent_id AND s.table_name = TG_TABLE_NAME;
        NEW := jsonb_populate_record(NEW, jsonb_build_object(col, greatest(coalesce(top, 0), coalesce(retired, 0)) + 1));
    END IF;
    RETURN NEW;
END $$;

-- The largest id a statement deleted, so it is never handed out again (SQLite's sqlite_sequence).
CREATE OR REPLACE FUNCTION ctx.lcm_retire_ids() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    EXECUTE format('INSERT INTO ctx.lcm_id_sequences AS s (agent_id, table_name, last_id) '
                   'SELECT agent_id, %L, max(%I) FROM gone GROUP BY agent_id '
                   'ON CONFLICT (agent_id, table_name) DO UPDATE SET last_id = greatest(s.last_id, EXCLUDED.last_id)',
                   TG_TABLE_NAME, TG_ARGV[0]);
    RETURN NULL;
END $$;

CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('store_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.summary_nodes
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('node_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_rollups
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('rollup_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_rollup_invalidations
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('event_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_entity_aliases
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('alias_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_entity_keys
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('key_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_memory_events
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('event_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_teams_audit
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('event_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.message_relations
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('relation_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_trajectory_sources
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('source_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_trajectory_states
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('state_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_trajectory_assets
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('asset_id');
CREATE OR REPLACE TRIGGER lcm_assign_id BEFORE INSERT ON ctx.lcm_trajectory_transitions
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assign_id('transition_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_messages
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('store_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.summary_nodes
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('node_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_rollups
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('rollup_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_rollup_invalidations
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('event_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_entity_aliases
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('alias_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_entity_keys
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('key_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_memory_events
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('event_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_teams_audit
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('event_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_trajectory_sources
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('source_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_trajectory_states
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('state_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_trajectory_assets
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('asset_id');
CREATE OR REPLACE TRIGGER lcm_retire_ids AFTER DELETE ON ctx.lcm_trajectory_transitions
    REFERENCING OLD TABLE AS gone FOR EACH STATEMENT EXECUTE FUNCTION ctx.lcm_retire_ids('transition_id');

-- LCM-X's indexes, each led by agent_id.
CREATE INDEX IF NOT EXISTS idx_msg_session ON ctx.lcm_messages (agent_id, session_id, store_id);
CREATE INDEX IF NOT EXISTS idx_msg_session_ts ON ctx.lcm_messages (agent_id, session_id, timestamp);
CREATE INDEX IF NOT EXISTS idx_lcm_lifecycle_current_session ON ctx.lcm_lifecycle_state (agent_id, current_session_id);
CREATE INDEX IF NOT EXISTS idx_lcm_lifecycle_last_finalized_session ON ctx.lcm_lifecycle_state (agent_id, last_finalized_session_id);
CREATE INDEX IF NOT EXISTS idx_msg_conversation_session ON ctx.lcm_messages (agent_id, conversation_id, session_id, store_id);
CREATE INDEX IF NOT EXISTS idx_msg_source_session ON ctx.lcm_messages (agent_id, source, session_id, store_id);
CREATE INDEX IF NOT EXISTS idx_nodes_session_depth ON ctx.summary_nodes (agent_id, session_id, depth, created_at);
CREATE INDEX IF NOT EXISTS idx_nodes_session_node ON ctx.summary_nodes (agent_id, session_id, node_id);
CREATE INDEX IF NOT EXISTS idx_nodes_session_depth_node ON ctx.summary_nodes (agent_id, session_id, depth, node_id);
CREATE INDEX IF NOT EXISTS idx_nodes_session_latest ON ctx.summary_nodes (agent_id, session_id, latest_at, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lcm_assertion_sources_current ON ctx.lcm_assertion_sources (agent_id, source_store_id, extraction_version) WHERE invalidated_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_lcm_assertions_source ON ctx.lcm_assertions (agent_id, source_store_id, extraction_version, source_content_sha256);
CREATE INDEX IF NOT EXISTS idx_lcm_assertions_state ON ctx.lcm_assertions (agent_id, subject_key, predicate_key, kind, scope_key, observed_at DESC);
CREATE INDEX IF NOT EXISTS idx_lcm_assertion_relations_source ON ctx.lcm_assertion_relations (agent_id, source_store_id, extraction_version, source_content_sha256);
CREATE INDEX IF NOT EXISTS idx_lcm_assertion_relations_from ON ctx.lcm_assertion_relations (agent_id, from_assertion_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_lcm_assertion_relations_to ON ctx.lcm_assertion_relations (agent_id, to_assertion_id, relation_type);
CREATE INDEX IF NOT EXISTS idx_lcm_query_view_sources_store ON ctx.lcm_query_view_sources (agent_id, source_store_id, view_id, version);
CREATE INDEX IF NOT EXISTS idx_lcm_query_view_versions_coverage ON ctx.lcm_query_view_versions (agent_id, corpus_generation, view_id, version);
CREATE INDEX IF NOT EXISTS idx_lcm_query_views_lifecycle ON ctx.lcm_query_views (agent_id, status, expires_at, updated_at);
CREATE INDEX IF NOT EXISTS idx_lcm_embedding_profile_model ON ctx.lcm_embedding_profile (agent_id, model_name, provider);
CREATE INDEX IF NOT EXISTS idx_lcm_embedding_meta_identity_embedded_at ON ctx.lcm_embedding_meta (agent_id, identity_hash, embedded_at DESC) WHERE archived = 0;
CREATE INDEX IF NOT EXISTS idx_lcm_rollup_sources_node ON ctx.lcm_rollup_sources (agent_id, node_id);
CREATE INDEX IF NOT EXISTS idx_lcm_rollup_invalidations_pending ON ctx.lcm_rollup_invalidations (agent_id, event_id);
CREATE INDEX IF NOT EXISTS idx_lcm_rollup_invalidations_scope_event ON ctx.lcm_rollup_invalidations (agent_id, scope, event_id);
CREATE INDEX IF NOT EXISTS idx_lcm_rollup_invalidations_scope_coverage ON ctx.lcm_rollup_invalidations (agent_id, scope, covered_start, covered_end, event_id);
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_ready_period ON ctx.lcm_rollups (agent_id, scope, period_kind, period_start DESC) WHERE status = 'ready';
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_pending ON ctx.lcm_rollups (agent_id, scope, status, failed_at, period_start) WHERE status IN ('stale', 'failed');
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_expired_lease ON ctx.lcm_rollups (agent_id, lease_expires_at, rollup_id) WHERE status = 'building';
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_stale_day ON ctx.lcm_rollups (agent_id, scope, period_start) WHERE status = 'stale' AND period_kind = 'day';
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_stale_aggregate ON ctx.lcm_rollups (agent_id, scope, period_start, period_kind) WHERE status = 'stale' AND period_kind IN ('week', 'month');
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_failed_day ON ctx.lcm_rollups (agent_id, scope, failed_at, period_start) WHERE status = 'failed' AND period_kind = 'day';
CREATE INDEX IF NOT EXISTS idx_lcm_rollups_failed_aggregate ON ctx.lcm_rollups (agent_id, scope, failed_at, period_start, period_kind) WHERE status = 'failed' AND period_kind IN ('week', 'month');
CREATE INDEX IF NOT EXISTS idx_lcm_chunk_meta_identity_embedded_at ON ctx.lcm_chunk_meta (agent_id, identity_hash, embedded_at DESC) WHERE archived = 0;
CREATE INDEX IF NOT EXISTS idx_lcm_chunk_meta_store ON ctx.lcm_chunk_meta (agent_id, store_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lcm_entity_platform_id ON ctx.lcm_entity_aliases (agent_id, scope_key,alias_norm) WHERE alias_kind='platform_id' AND retired_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_lcm_entity_key_active ON ctx.lcm_entity_keys (agent_id, scope_key,subject_key,coalesce(owner_entity_id,'')) WHERE retired_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_lcm_memory_host_file_rev_content ON ctx.lcm_memory_host_file_revisions (agent_id, profile_key,path,content_hash);
CREATE INDEX IF NOT EXISTS idx_lcm_teams_principals_tenant ON ctx.lcm_teams_principals (agent_id, tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_lcm_teams_collections_tenant ON ctx.lcm_teams_collections (agent_id, tenant_id, kind);
CREATE INDEX IF NOT EXISTS idx_lcm_teams_audit_time ON ctx.lcm_teams_audit (agent_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_msg_conversation_observed ON ctx.lcm_messages (agent_id, conversation_id, observed_at);
CREATE INDEX IF NOT EXISTS idx_msg_relations_store ON ctx.message_relations (agent_id, store_id, kind);
CREATE INDEX IF NOT EXISTS idx_msg_relations_related ON ctx.message_relations (agent_id, related_store_id, kind);
CREATE INDEX IF NOT EXISTS idx_msg_relations_observed ON ctx.message_relations (agent_id, kind, observed_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_host_uid_canonical ON ctx.host_uid_bindings (agent_id, lineage_key, uid) WHERE kind = 'canonical';
CREATE INDEX IF NOT EXISTS idx_host_uid_store ON ctx.host_uid_bindings (agent_id, store_id);
CREATE INDEX IF NOT EXISTS idx_host_uid_lineage_uid ON ctx.host_uid_bindings (agent_id, lineage_key, uid);
CREATE INDEX IF NOT EXISTS lcm_trajectory_states_source_sequence ON ctx.lcm_trajectory_states (agent_id, source_id, sequence_ordinal);
CREATE UNIQUE INDEX IF NOT EXISTS lcm_trajectory_embedding_one_active ON ctx.lcm_trajectory_embedding_profiles (agent_id, active) WHERE active = 1;
CREATE INDEX IF NOT EXISTS lcm_trajectory_embeddings_profile ON ctx.lcm_trajectory_embeddings (agent_id, profile_digest, source_id);
CREATE UNIQUE INDEX IF NOT EXISTS lcm_trajectory_state_embedding_one_active ON ctx.lcm_trajectory_state_embedding_profiles (agent_id, active) WHERE active = 1;
CREATE INDEX IF NOT EXISTS lcm_trajectory_state_embeddings_profile ON ctx.lcm_trajectory_state_embeddings (agent_id, profile_digest, state_id);

-- LCM's messages: the placement row and its text. security_invoker: both tables' row-level security applies.
-- content/tool_calls give back the exact text LCM stored (memory.messages.content is its plain-text form).
CREATE OR REPLACE VIEW ctx.messages WITH (security_invoker = true) AS
SELECT p.store_id,
       p.session_id,
       p.source,
       p.conversation_id,
       m.role,
       CASE WHEN m.native->'content_raw'->>0 = 's' THEN m.native->'content_raw'->>1 ELSE m.content END AS content,
       m.tool_call_id,
       CASE WHEN m.native->'tool_calls_raw'->>0 = 's' THEN m.native->'tool_calls_raw'->>1
            ELSE m.tool_calls::text END AS tool_calls,
       m.tool_name,
       p.timestamp,
       p.token_estimate,
       p.pinned,
       p.ingested_at,
       p.observed_at,
       p.observed_at_source,
       p.sender_id,
       p.sender_source,
       p.access_scope,
       p.text_uid,
       p.content_sha256,
       p.store_id AS rowid
FROM ctx.lcm_messages AS p
JOIN memory.messages AS m ON m.uid = p.text_uid;

-- The FTS5 tables LCM queries, as views over the Postgres indexes: memory.messages.tsv and its pg_trgm index
-- serve messages_fts; the expression indexes below serve nodes_fts and lcm_trajectory_states_fts. Each tokenizes
-- with ctx.lcm_unicode61, as FTS5 does, so the indexes are expressions over it.
-- FTS5's unicode61 tokenizer: a token is a run of letters, digits and private-use characters (accents removed);
-- everything else separates it.
-- pg_c_utf8 classifies by Unicode whatever the database's locale; to_tsvector('simple', ...) then lower-cases.
CREATE OR REPLACE FUNCTION ctx.lcm_unicode61(body text) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT regexp_replace(regexp_replace(normalize(body, NFD) COLLATE "pg_c_utf8", '[\u0300-\u036f]', '', 'g'),
                          '[^[:alnum:]\ue000-\uf8ff\U000f0000-\U000ffffd\U00100000-\U0010fffd]+', ' ', 'g')
$$;
CREATE OR REPLACE VIEW ctx.messages_fts WITH (security_invoker = true) AS
SELECT p.store_id AS rowid, m.content, to_tsvector('simple', ctx.lcm_unicode61(m.content)) AS tsv
FROM ctx.lcm_messages AS p
JOIN memory.messages AS m ON m.uid = p.text_uid;
-- messages_fts_stem is FTS5's porter tokenizer on SQLite: words lower-cased and stemmed, no stop words. Postgres's
-- snowball English stemmer without its stop word list, over the simple parser.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_ts_dict WHERE dictname = 'lcm_english_stem'
                   AND dictnamespace = 'ctx'::regnamespace) THEN
        CREATE TEXT SEARCH DICTIONARY ctx.lcm_english_stem (TEMPLATE = snowball, LANGUAGE = english);
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_ts_config WHERE cfgname = 'lcm_porter' AND cfgnamespace = 'ctx'::regnamespace) THEN
        CREATE TEXT SEARCH CONFIGURATION ctx.lcm_porter (COPY = pg_catalog.simple);
        ALTER TEXT SEARCH CONFIGURATION ctx.lcm_porter
            ALTER MAPPING FOR asciiword, asciihword, hword_asciipart, word, hword, hword_part WITH ctx.lcm_english_stem;
    END IF;
END $$;
CREATE OR REPLACE VIEW ctx.messages_fts_stem WITH (security_invoker = true) AS
SELECT p.store_id AS rowid, m.content, to_tsvector('ctx.lcm_porter'::regconfig, ctx.lcm_unicode61(m.content)) AS tsv
FROM ctx.lcm_messages AS p
JOIN memory.messages AS m ON m.uid = p.text_uid;
CREATE INDEX IF NOT EXISTS messages_content_lcm_porter
    ON memory.messages USING gin (to_tsvector('ctx.lcm_porter'::regconfig, ctx.lcm_unicode61(content)));
-- FTS5's rank is bm25 (k1 = 1.2, b = 0.75) over the query's phrases: idf from how many rows hold each phrase (1e-6
-- when that is half the rows or more, as FTS5 floors it), the phrase's instances in the row, and the row's length in
-- tokens against the table's average. *stats* is [rows, tokens, rows holding phrase 1, ...] for the whole table.
CREATE OR REPLACE FUNCTION ctx.lcm_tsv_tokens(tsv tsvector) RETURNS bigint
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    SELECT coalesce(sum(coalesce(array_length(u.positions, 1), 1)), 0) FROM unnest(tsv) AS u
$$;
CREATE OR REPLACE FUNCTION ctx.lcm_phrase_words(cfg regconfig, phrase text) RETURNS text[]
    LANGUAGE sql STABLE PARALLEL SAFE STRICT AS $$
    SELECT coalesce(array_agg(u.lexeme ORDER BY p), '{}')
    FROM unnest(to_tsvector(cfg, ctx.lcm_unicode61(phrase))) AS u, unnest(u.positions) AS p
$$;
CREATE OR REPLACE FUNCTION ctx.lcm_phrase_hits(tsv tsvector, words text[], prefix boolean) RETURNS bigint
    LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
    WITH pos AS (
        SELECT u.lexeme, p::int AS p FROM unnest(tsv) AS u, unnest(u.positions) AS p
    )
    SELECT count(*) FROM pos AS first
    WHERE cardinality(words) > 0
      AND (first.lexeme = words[1] OR (prefix AND cardinality(words) = 1 AND starts_with(first.lexeme, words[1])))
      AND NOT EXISTS (
          SELECT 1 FROM generate_series(2, cardinality(words)) AS k
          WHERE NOT EXISTS (
              SELECT 1 FROM pos AS nxt
              WHERE nxt.p = first.p + k - 1
                AND (nxt.lexeme = words[k] OR (prefix AND k = cardinality(words) AND starts_with(nxt.lexeme, words[k])))))
$$;
CREATE OR REPLACE FUNCTION ctx.lcm_bm25(tsv tsvector, cfg regconfig, phrases text[], prefixes boolean[],
                                        stats double precision[]) RETURNS double precision
    LANGUAGE plpgsql STABLE PARALLEL SAFE AS $$
DECLARE
    total double precision := 0;
    dl double precision := ctx.lcm_tsv_tokens(tsv);
    avgdl double precision;
    hits double precision;
    held double precision;
    idf double precision;
BEGIN
    IF tsv IS NULL OR coalesce(stats[1], 0) <= 0 THEN
        RETURN 0;
    END IF;
    avgdl := greatest(stats[2] / stats[1], 1e-9);
    FOR i IN 1 .. coalesce(cardinality(phrases), 0) LOOP
        held := stats[2 + i];
        idf := ln((stats[1] - held + 0.5) / (held + 0.5));
        IF idf <= 0 THEN
            idf := 1e-6;
        END IF;
        hits := ctx.lcm_phrase_hits(tsv, ctx.lcm_phrase_words(cfg, phrases[i]), prefixes[i]);
        total := total + idf * (hits * 2.2) / (hits + 1.2 * (0.25 + 0.75 * dl / avgdl));
    END LOOP;
    RETURN -total;
END $$;

CREATE OR REPLACE VIEW ctx.nodes_fts WITH (security_invoker = true) AS
SELECT node_id AS rowid, summary, to_tsvector('simple', ctx.lcm_unicode61(summary)) AS tsv FROM ctx.summary_nodes;
CREATE OR REPLACE VIEW ctx.lcm_trajectory_states_fts WITH (security_invoker = true) AS
SELECT state_id AS rowid, search_text, to_tsvector('simple', ctx.lcm_unicode61(search_text)) AS tsv FROM ctx.lcm_trajectory_states;

CREATE INDEX IF NOT EXISTS summary_nodes_tsv ON ctx.summary_nodes USING gin (to_tsvector('simple', ctx.lcm_unicode61(summary)));
CREATE INDEX IF NOT EXISTS summary_nodes_summary_trgm ON ctx.summary_nodes USING gin (summary gin_trgm_ops);
CREATE INDEX IF NOT EXISTS lcm_trajectory_states_tsv
    ON ctx.lcm_trajectory_states USING gin (to_tsvector('simple', ctx.lcm_unicode61(search_text)));
CREATE INDEX IF NOT EXISTS lcm_trajectory_states_search_trgm
    ON ctx.lcm_trajectory_states USING gin (search_text gin_trgm_ops);
CREATE INDEX IF NOT EXISTS messages_content_lcm_unicode61
    ON memory.messages USING gin (to_tsvector('simple', ctx.lcm_unicode61(content)));
CREATE INDEX IF NOT EXISTS lcm_messages_text_uid ON ctx.lcm_messages (text_uid);

-- PRAGMA table_info(t) / pragma_table_info(t): LCM-X's columns of a ctx table or view (agent_id is the store's).
CREATE OR REPLACE FUNCTION ctx.pragma_table_info(relation text)
    RETURNS TABLE (cid integer, name text, type text, "notnull" integer, dflt_value text, pk integer)
    LANGUAGE sql STABLE AS $$
    SELECT (row_number() OVER (ORDER BY a.attnum) - 1)::integer, a.attname::text,
           upper(CASE format_type(a.atttypid, NULL) WHEN 'bigint' THEN 'INTEGER' WHEN 'double precision' THEN 'REAL'
                 WHEN 'bytea' THEN 'BLOB' ELSE 'TEXT' END),
           a.attnotnull::integer, pg_get_expr(d.adbin, d.adrelid),
           coalesce((SELECT array_position(i.indkey::int2[], a.attnum) FROM pg_index AS i
                      WHERE i.indrelid = c.oid AND i.indisprimary), 0)::integer
      FROM pg_class AS c
      JOIN pg_attribute AS a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
      LEFT JOIN pg_attrdef AS d ON d.adrelid = c.oid AND d.adnum = a.attnum
     WHERE c.relnamespace = 'ctx'::regnamespace AND c.relname = relation AND a.attname NOT IN ('agent_id', 'rowid')
       -- the store's own columns of the messages view (where its text lives), which LCM-X's table does not have
       AND NOT (relation = 'messages' AND a.attname IN ('text_uid', 'content_sha256'))
       -- access_scope is on every ctx table, but on SQLite LCM-X adds it with its scope_v1 migration: until this agent
       -- has run that step, the column is not listed (LCM-X then runs the step, which on the store only records it).
       AND NOT (a.attname = 'access_scope'
                AND NOT EXISTS (SELECT 1 FROM ctx.lcm_migration_state AS s WHERE s.step_name = 'scope_v1'))
       AND NOT (relation IN ('messages_fts', 'messages_fts_stem', 'nodes_fts', 'lcm_trajectory_states_fts')
                AND a.attname = 'tsv')
     ORDER BY a.attnum $$;

-- sqlite_master for LCM's existence checks ("is this table there"): the ctx tables, views, indexes and triggers.
CREATE OR REPLACE VIEW ctx.sqlite_master AS
-- Every ctx view stands in for a table LCM-X makes on SQLite (messages, the FTS5 tables, the vector tables), so
-- it reads as a table; LCM-X itself creates no views.
SELECT CASE c.relkind WHEN 'i' THEN 'index' ELSE 'table' END AS type,
       c.relname::text AS name,
       coalesce(t.relname, c.relname)::text AS tbl_name,
       0 AS rootpage,
       NULL::text AS sql
FROM pg_class AS c
LEFT JOIN pg_index AS i ON i.indexrelid = c.oid
LEFT JOIN pg_class AS t ON t.oid = i.indrelid
WHERE c.relnamespace = 'ctx'::regnamespace AND c.relkind IN ('r', 'v', 'i', 'p') AND c.relname <> 'sqlite_master'
UNION ALL
SELECT 'trigger', tg.tgname::text, c.relname::text, 0, NULL::text
FROM pg_trigger AS tg
JOIN pg_class AS c ON c.oid = tg.tgrelid
WHERE c.relnamespace = 'ctx'::regnamespace AND NOT tg.tgisinternal;

-- LCM-X's SQLite triggers, with the same effect.

-- Rollups: every summary node change queues an invalidation of the days it covers.
CREATE OR REPLACE FUNCTION ctx.lcm_rollup_node_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        INSERT INTO ctx.lcm_rollup_invalidations (node_id, scope, covered_start, covered_end, operation)
        VALUES (OLD.node_id, OLD.session_id,
                least(coalesce(OLD.earliest_at, OLD.created_at), coalesce(OLD.latest_at, OLD.created_at)),
                greatest(coalesce(OLD.earliest_at, OLD.created_at), coalesce(OLD.latest_at, OLD.created_at)),
                lower(TG_OP));
    END IF;
    IF TG_OP IN ('INSERT', 'UPDATE') THEN
        INSERT INTO ctx.lcm_rollup_invalidations (node_id, scope, covered_start, covered_end, operation)
        VALUES (NEW.node_id, NEW.session_id,
                least(coalesce(NEW.earliest_at, NEW.created_at), coalesce(NEW.latest_at, NEW.created_at)),
                greatest(coalesce(NEW.earliest_at, NEW.created_at), coalesce(NEW.latest_at, NEW.created_at)),
                lower(TG_OP));
    END IF;
    -- A BEFORE trigger that returns NULL would cancel the delete.
    IF TG_WHEN = 'BEFORE' THEN
        RETURN OLD;
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_rollup_node_insert AFTER INSERT ON ctx.summary_nodes
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_rollup_node_change();
CREATE OR REPLACE TRIGGER lcm_rollup_node_delete BEFORE DELETE ON ctx.summary_nodes
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_rollup_node_change();
CREATE OR REPLACE TRIGGER lcm_rollup_node_update
    AFTER UPDATE OF session_id, depth, summary, token_count, source_token_count, source_ids, source_type, created_at,
                    earliest_at, latest_at, expand_hint ON ctx.summary_nodes
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_rollup_node_change();

-- Chunk gap-fill scans restart when a chunk or the text of a chunked message changes.
CREATE OR REPLACE FUNCTION ctx.lcm_chunk_gapfill_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM ctx.metadata WHERE key IN ('chunk_gapfill_scan:' || OLD.identity_hash,
                                           'chunk_policy_revision_through:' || OLD.identity_hash);
    IF TG_OP = 'UPDATE' THEN
        DELETE FROM ctx.metadata WHERE key IN ('chunk_gapfill_scan:' || NEW.identity_hash,
                                               'chunk_policy_revision_through:' || NEW.identity_hash);
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_chunk_gapfill_delete AFTER DELETE ON ctx.lcm_chunk_meta
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_chunk_gapfill_change();
CREATE OR REPLACE TRIGGER lcm_chunk_gapfill_update
    AFTER UPDATE OF store_id, identity_hash, chunk_id, char_start, char_end ON ctx.lcm_chunk_meta
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_chunk_gapfill_change();

-- A message's text changes only by pointing at another text row (role never changes on Postgres).
CREATE OR REPLACE FUNCTION ctx.lcm_message_text_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' OR OLD.text_uid IS DISTINCT FROM NEW.text_uid THEN
        UPDATE ctx.lcm_assertion_sources
           SET invalidated_at = ctx.lcm_now(),
               invalidation_reason = CASE TG_OP WHEN 'DELETE' THEN 'source_deleted' ELSE 'source_updated' END
         WHERE source_store_id = OLD.store_id AND invalidated_at IS NULL;
    END IF;
    IF TG_OP = 'UPDATE' AND OLD.text_uid IS DISTINCT FROM NEW.text_uid THEN
        DELETE FROM ctx.metadata WHERE key LIKE 'chunk_gapfill_scan:%' OR key LIKE 'chunk_policy_revision_through:%';
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_assertion_message_delete AFTER DELETE ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_message_text_change();
CREATE OR REPLACE TRIGGER lcm_assertion_message_update AFTER UPDATE OF text_uid ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_message_text_change();

-- Query views: the corpus generation counts every message change, and a change to a view's source stales it.
CREATE OR REPLACE FUNCTION ctx.lcm_query_corpus_change() RETURNS trigger
    LANGUAGE plpgsql AS $$
DECLARE
    r ctx.lcm_messages%ROWTYPE;
    role_now text;
BEGIN
    IF TG_OP = 'DELETE' THEN
        r := OLD;
    ELSE
        r := NEW;
    END IF;
    SELECT m.role INTO role_now FROM memory.messages AS m WHERE m.uid = r.text_uid;
    UPDATE ctx.lcm_query_corpus_state
       SET generation = generation + 1,
           max_store_id = CASE WHEN TG_OP = 'DELETE' THEN max_store_id ELSE greatest(max_store_id, NEW.store_id) END,
           row_count = CASE TG_OP WHEN 'INSERT' THEN row_count + 1
                                  WHEN 'DELETE' THEN greatest(0, row_count - 1) ELSE row_count END,
           changed_at = ctx.lcm_now()
     WHERE singleton = 1;
    INSERT INTO ctx.lcm_query_corpus_events (generation, store_id, mutation, session_id, conversation_id, source, role,
                                             source_timestamp, changed_at)
    SELECT generation, r.store_id, lower(TG_OP), r.session_id, coalesce(r.conversation_id, ''),
           coalesce(r.source, ''), coalesce(role_now, ''), r.timestamp, changed_at
      FROM ctx.lcm_query_corpus_state WHERE singleton = 1;
    IF TG_OP IN ('DELETE', 'UPDATE') THEN
        UPDATE ctx.lcm_query_views AS v
           SET status = 'stale', generation = v.generation + 1,
               stale_reason = CASE TG_OP WHEN 'DELETE' THEN 'positive_source_deleted'
                                         ELSE 'positive_source_updated' END,
               build_nonce = '', lease_expires_at = NULL, updated_at = ctx.lcm_now()
         WHERE v.status IN ('ready', 'building')
           AND v.view_id IN (SELECT dependency.view_id FROM ctx.lcm_query_view_sources AS dependency
                              WHERE dependency.source_store_id = OLD.store_id
                                AND dependency.version = v.current_version);
    END IF;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_query_corpus_message_insert AFTER INSERT ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_query_corpus_change();
CREATE OR REPLACE TRIGGER lcm_query_corpus_message_update
    AFTER UPDATE OF text_uid, session_id, conversation_id, source, timestamp ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_query_corpus_change();
CREATE OR REPLACE TRIGGER lcm_query_corpus_message_delete AFTER DELETE ON ctx.lcm_messages
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_query_corpus_change();

-- Assertions must quote their source message exactly.
CREATE OR REPLACE FUNCTION ctx.lcm_assertion_source_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM ctx.messages AS m
                    WHERE m.store_id = NEW.source_store_id AND m.session_id = NEW.source_session_id
                      AND m.role = NEW.source_role AND m.source = NEW.source_name
                      AND coalesce(m.observed_at, m.timestamp) = NEW.source_timestamp) THEN
        PERFORM ctx.lcm_raise('assertion source row is missing or metadata changed');
    END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_assertion_source_insert_guard BEFORE INSERT ON ctx.lcm_assertion_sources
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assertion_source_guard();

CREATE OR REPLACE FUNCTION ctx.lcm_assertion_quotes_source(store_id bigint, version text, sha text,
                                                           span_start bigint, span_end bigint, quote text)
    RETURNS boolean LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM ctx.messages AS m
        JOIN ctx.lcm_assertion_sources AS s
          ON s.source_store_id = store_id AND s.extraction_version = version
         AND s.source_content_sha256 = sha AND s.invalidated_at IS NULL
        WHERE m.store_id = store_id
          AND span_end <= length(coalesce(m.content, ''))
          AND substr(coalesce(m.content, ''), (span_start + 1)::int, (span_end - span_start)::int) = quote) $$;
CREATE OR REPLACE FUNCTION ctx.lcm_assertion_is_current(id text) RETURNS boolean
    LANGUAGE sql STABLE AS $$
    SELECT EXISTS (
        SELECT 1 FROM ctx.lcm_assertions AS endpoint
        JOIN ctx.lcm_assertion_sources AS endpoint_source
          ON endpoint_source.source_store_id = endpoint.source_store_id
         AND endpoint_source.extraction_version = endpoint.extraction_version
         AND endpoint_source.source_content_sha256 = endpoint.source_content_sha256
         AND endpoint_source.invalidated_at IS NULL
        WHERE endpoint.assertion_id = id) $$;
CREATE OR REPLACE FUNCTION ctx.lcm_assertion_row_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NOT ctx.lcm_assertion_quotes_source(NEW.source_store_id, NEW.extraction_version, NEW.source_content_sha256,
                                           NEW.source_span_start, NEW.source_span_end, NEW.source_quote) THEN
        PERFORM ctx.lcm_raise('assertion source provenance mismatch');
    END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_assertion_row_insert_guard BEFORE INSERT ON ctx.lcm_assertions
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assertion_row_guard();
CREATE OR REPLACE FUNCTION ctx.lcm_assertion_relation_guard() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    IF NOT (ctx.lcm_assertion_quotes_source(NEW.source_store_id, NEW.extraction_version, NEW.source_content_sha256,
                                            NEW.source_span_start, NEW.source_span_end, NEW.source_quote)
            AND ctx.lcm_assertion_is_current(NEW.from_assertion_id)
            AND ctx.lcm_assertion_is_current(NEW.to_assertion_id)) THEN
        PERFORM ctx.lcm_raise('assertion relation provenance mismatch');
    END IF;
    RETURN NEW;
END $$;
CREATE OR REPLACE TRIGGER lcm_assertion_relation_insert_guard BEFORE INSERT ON ctx.lcm_assertion_relations
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assertion_relation_guard();

CREATE OR REPLACE FUNCTION ctx.lcm_assertion_source_deleted() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM ctx.lcm_assertion_relations
     WHERE (source_store_id = OLD.source_store_id AND extraction_version = OLD.extraction_version
            AND source_content_sha256 = OLD.source_content_sha256)
        OR from_assertion_id IN (SELECT assertion_id FROM ctx.lcm_assertions
                                  WHERE source_store_id = OLD.source_store_id
                                    AND extraction_version = OLD.extraction_version
                                    AND source_content_sha256 = OLD.source_content_sha256)
        OR to_assertion_id IN (SELECT assertion_id FROM ctx.lcm_assertions
                                WHERE source_store_id = OLD.source_store_id
                                  AND extraction_version = OLD.extraction_version
                                  AND source_content_sha256 = OLD.source_content_sha256);
    DELETE FROM ctx.lcm_assertions
     WHERE source_store_id = OLD.source_store_id AND extraction_version = OLD.extraction_version
       AND source_content_sha256 = OLD.source_content_sha256;
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_assertion_source_delete AFTER DELETE ON ctx.lcm_assertion_sources
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_assertion_source_deleted();

-- The memory event log is append-only.
CREATE OR REPLACE FUNCTION ctx.lcm_memory_events_append_only() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    PERFORM ctx.lcm_raise('lcm_memory_events is append-only');
    RETURN NULL;
END $$;
CREATE OR REPLACE TRIGGER lcm_memory_events_no_update BEFORE UPDATE ON ctx.lcm_memory_events
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_memory_events_append_only();
CREATE OR REPLACE TRIGGER lcm_memory_events_no_delete BEFORE DELETE ON ctx.lcm_memory_events
    FOR EACH ROW EXECUTE FUNCTION ctx.lcm_memory_events_append_only();

-- LCM-X names an id outright at times (SQLite then allocates past it); moving the sequence takes UPDATE.
GRANT UPDATE ON ALL SEQUENCES IN SCHEMA ctx TO lina_app;
-- LCM-X writes thousands of rows in one transaction and reads them right after; lina_app refreshes the planner's
-- statistics then (lcm_x_store), instead of waiting for autovacuum.
GRANT MAINTAIN ON ALL TABLES IN SCHEMA ctx TO lina_app;
