-- The nightly brain cycle (lina#444, design lina#438 "dream cycle"): proposals, cycle records, the cycle lock and the
-- counters (hermes_cli/brain/dream/, docs/brain.md "The nightly cycle").
--
-- brain.proposals from 0014 is reshaped: every change a cycle phase wants to make to compiled truth, a merge or a
-- relation is one row here until `lina brain proposals accept|reject` decides it. `kind` is open text, so later
-- writers (promotions of external items, lina#441) add kinds without a migration. Nothing wrote 0014's shape, so its
-- state 'open' maps to 'proposed' and page_slug becomes target.

DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'brain' AND table_name = 'proposals' AND column_name = 'state') THEN
        ALTER TABLE brain.proposals RENAME COLUMN state TO status;
    END IF;
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_schema = 'brain' AND table_name = 'proposals' AND column_name = 'page_slug') THEN
        ALTER TABLE brain.proposals RENAME COLUMN page_slug TO target;
    END IF;
END $$;
ALTER TABLE brain.proposals DROP CONSTRAINT IF EXISTS proposals_kind_check;
ALTER TABLE brain.proposals DROP CONSTRAINT IF EXISTS proposals_state_check;
ALTER TABLE brain.proposals DROP CONSTRAINT IF EXISTS proposals_status_check;
UPDATE brain.proposals SET status = 'proposed' WHERE status = 'open';
ALTER TABLE brain.proposals ALTER COLUMN status SET DEFAULT 'proposed';
ALTER TABLE brain.proposals ADD CONSTRAINT proposals_status_check
    CHECK (status IN ('proposed', 'accepted', 'rejected', 'applied'));

-- phase: the cycle phase (or other writer) that made it. grade: the lowest grade of the material it is built from
-- (lina#440); an 'external' proposal is accepted only with --confirm-external. source_refs: the timeline entries,
-- atoms or files it cites. fingerprint: the same proposal from a later night is not written again, decided or not.
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS phase text;
UPDATE brain.proposals SET phase = kind WHERE phase IS NULL;
ALTER TABLE brain.proposals ALTER COLUMN phase SET NOT NULL;
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS grade text NOT NULL DEFAULT 'external';
ALTER TABLE brain.proposals DROP CONSTRAINT IF EXISTS proposals_grade_check;
ALTER TABLE brain.proposals ADD CONSTRAINT proposals_grade_check CHECK (grade IN ('owner', 'agent', 'external'));
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS source_refs text[] NOT NULL DEFAULT '{}';
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS cycle_id bigint;
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS fingerprint text;
ALTER TABLE brain.proposals ADD COLUMN IF NOT EXISTS decided_by text;
DROP INDEX IF EXISTS brain.proposals_open;
CREATE INDEX IF NOT EXISTS proposals_status ON brain.proposals (agent_id, status, phase);
CREATE UNIQUE INDEX IF NOT EXISTS proposals_fingerprint ON brain.proposals (agent_id, fingerprint);

-- One run of `lina brain dream` for one agent: what it found and what it spent. The counters of lina#444 are read
-- from these rows (view dream_metrics below).
CREATE TABLE IF NOT EXISTS brain.dream_cycles (
    cycle_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    pages int NOT NULL DEFAULT 0,
    orphans int NOT NULL DEFAULT 0,
    new_pages int NOT NULL DEFAULT 0,
    new_pages_linked int NOT NULL DEFAULT 0,
    new_links int NOT NULL DEFAULT 0,
    calls int NOT NULL DEFAULT 0,
    input_chars bigint NOT NULL DEFAULT 0,
    output_chars bigint NOT NULL DEFAULT 0,
    report jsonb NOT NULL DEFAULT '{}'::jsonb
);

-- The cycle in which an agent's cycle first saw each page: "new pages" of a cycle are those first seen in it.
CREATE TABLE IF NOT EXISTS brain.dream_pages_seen (
    agent_id text NOT NULL,
    slug text NOT NULL,
    first_cycle_id bigint NOT NULL REFERENCES brain.dream_cycles (cycle_id) ON DELETE CASCADE,
    PRIMARY KEY (agent_id, slug)
);

-- The digest of the input a model phase last answered for one subject (a page, a pair, a batch): an unchanged
-- input is not sent again on a later night, so a quiet brain costs nothing.
CREATE TABLE IF NOT EXISTS brain.dream_inputs (
    agent_id text NOT NULL,
    phase text NOT NULL,
    subject text NOT NULL,
    digest text NOT NULL,
    cycle_id bigint REFERENCES brain.dream_cycles (cycle_id) ON DELETE SET NULL,
    PRIMARY KEY (agent_id, phase, subject)
);

-- The cycle lock: phases that write the database hold it; a holder that died leaves it to expire. Shared (no
-- agent_id): one cycle at a time writes the brain.
CREATE TABLE IF NOT EXISTS brain.cycle_lock (
    name text PRIMARY KEY,
    holder text NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

GRANT SELECT ON brain.dream_cycles, brain.dream_pages_seen, brain.dream_inputs TO lina_app;

-- Acceptance per phase: accepted and applied count as accepted; the rate is over decided proposals only.
CREATE OR REPLACE VIEW brain.proposal_acceptance WITH (security_invoker = true) AS
SELECT agent_id, phase,
       count(*) AS proposals,
       count(*) FILTER (WHERE status = 'proposed') AS open,
       count(*) FILTER (WHERE status IN ('accepted', 'applied')) AS accepted,
       count(*) FILTER (WHERE status = 'applied') AS applied,
       count(*) FILTER (WHERE status = 'rejected') AS rejected,
       round((count(*) FILTER (WHERE status IN ('accepted', 'applied')))::numeric
             / nullif(count(*) FILTER (WHERE status <> 'proposed'), 0), 3) AS acceptance_rate
FROM brain.proposals
GROUP BY agent_id, phase;

-- One row per finished cycle: orphan rate, share of the pages first seen in the cycle that had a link by its end,
-- and what it cost (model calls, characters in and out).
CREATE OR REPLACE VIEW brain.dream_metrics WITH (security_invoker = true) AS
SELECT c.agent_id, c.cycle_id, c.started_at, c.finished_at, c.pages, c.orphans,
       round(c.orphans::numeric / nullif(c.pages, 0), 3) AS orphan_rate,
       c.new_pages, c.new_pages_linked,
       round(c.new_pages_linked::numeric / nullif(c.new_pages, 0), 3) AS new_linked_share,
       c.new_links, c.calls, c.input_chars, c.output_chars,
       (SELECT count(*) FROM brain.proposals AS p WHERE p.cycle_id = c.cycle_id) AS proposals
FROM brain.dream_cycles AS c
WHERE c.finished_at IS NOT NULL;

GRANT SELECT ON brain.proposal_acceptance, brain.dream_metrics TO lina_app;
