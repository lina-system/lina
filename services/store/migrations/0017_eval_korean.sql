-- The labeled Korean set (lina#377): sentences the owner flagged, each with the flagged phrase, and texts a
-- person marked "nothing to fix", scored by `lina eval korean` (hermes_cli/korean/eval.py, docs/korean.md). The set
-- lives here only, never in the repository. agent_id is the agent that wrote the text (NULL when unknown or not an
-- agent's), so the store's event trigger gives the table row-level security (lina_agent_rows, docs/store.md): an
-- agent sees and writes only its own texts, no agent sees an unowned one, and only the owner role `lina` that the
-- command runs as reads the whole set.
CREATE TABLE IF NOT EXISTS memory.korean_eval_texts (
    text_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('flagged', 'clean')),
    text text NOT NULL CHECK (text <> ''),
    text_sha256 text NOT NULL,
    -- flagged only: the flagged words, found verbatim in text.
    phrase text,
    -- clean only: who said there is nothing to fix. A reply the owner simply let pass is not a clean label.
    marked_by text CHECK (marked_by IN ('owner', 'reviewer')),
    -- The model family of the reviewer agent that marked it and of the agent that wrote it (lowercase).
    marker_family text,
    writer_family text,
    agent_id text,
    source text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT korean_eval_texts_label CHECK (
        (kind = 'flagged' AND phrase <> '' AND strpos(text, phrase) > 0 AND marked_by IS NULL)
        OR (kind = 'clean' AND phrase IS NULL AND marked_by IS NOT NULL)),
    -- A reviewer's "nothing to fix" counts only from a model family other than the writer's.
    CONSTRAINT korean_eval_texts_reviewer CHECK (
        marked_by IS DISTINCT FROM 'reviewer'
        OR (marker_family <> '' AND writer_family <> '' AND marker_family <> writer_family)),
    CONSTRAINT korean_eval_texts_owner CHECK (marked_by IS DISTINCT FROM 'owner' OR marker_family IS NULL)
);

-- One row per labeled text (and per flagged phrase of a sentence): an import run again adds nothing.
CREATE UNIQUE INDEX IF NOT EXISTS korean_eval_texts_once
    ON memory.korean_eval_texts (kind, text_sha256, coalesce(phrase, ''));

-- The set is the owner's yardstick: an agent reads its own texts and never writes one (the owner role `lina`
-- imports them), so no agent can label its own reply clean.
REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON memory.korean_eval_texts FROM lina_app;

-- Saved per-check numbers of one run (`lina eval korean --save-baseline NAME`): counts only, never text.
CREATE TABLE IF NOT EXISTS memory.korean_eval_baselines (
    name text PRIMARY KEY CHECK (name <> ''),
    counts jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON memory.korean_eval_baselines FROM lina_app;
