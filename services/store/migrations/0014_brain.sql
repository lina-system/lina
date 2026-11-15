-- The brain (lina#442, design lina#439 sections 1 and 3): one linked knowledge index over markdown pages.
-- The markdown files stay the original; every table here except merges, atoms and proposals is rebuilt from
-- them by `lina brain reindex` (hermes_cli/brain/). docs/brain.md describes the page model.
--
-- Shared or agent-scoped: the brain is one brain for every agent (lina#439: "one brain with trust grades").
-- Pages, aliases, edges, timeline entries, atoms and merges therefore carry no agent_id: every agent reads them
-- and only the owner tools (`lina brain`, as the store owner) write them. Proposals are written by an agent's
-- cycle, so they carry agent_id and get lina_agent_rows like every other agent table.

CREATE SCHEMA IF NOT EXISTS brain;

-- Row-level security on agent_id now covers brain too: the event trigger from services/store/initdb only looked
-- at memory, ctx, ltm and vec, so a brain table with agent_id would have been readable by every agent.
CREATE OR REPLACE FUNCTION public.lina_agent_rls_on_ddl() RETURNS event_trigger
    LANGUAGE plpgsql AS $$
DECLARE
    obj record;
BEGIN
    FOR obj IN
        SELECT objid FROM pg_event_trigger_ddl_commands()
        WHERE object_type = 'table' AND schema_name IN ('memory', 'ctx', 'ltm', 'vec', 'brain')
    LOOP
        IF EXISTS (SELECT 1 FROM pg_attribute
                   WHERE attrelid = obj.objid AND attname = 'agent_id' AND NOT attisdropped) THEN
            PERFORM public.lina_apply_agent_rls(obj.objid::regclass);
        END IF;
    END LOOP;
END $$;

-- One imported directory and the folder-to-type mapping it was imported with (reindex reuses it).
CREATE TABLE IF NOT EXISTS brain.roots (
    root_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    path text NOT NULL UNIQUE,
    mapping jsonb NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now()
);

-- One markdown file. page_id is derived from the slug (hermes_cli/brain/page.py page_id), so a reindex gives a
-- page the same id and its vec.embeddings row (kind 'page') stays valid. No status or deadline: the work ledger
-- owns them, and the frontmatter copy here leaves those keys out.
CREATE TABLE IF NOT EXISTS brain.pages (
    slug text PRIMARY KEY CHECK (slug <> '' AND slug IS NFC NORMALIZED),
    page_id bigint NOT NULL UNIQUE,
    root_id bigint NOT NULL REFERENCES brain.roots (root_id) ON DELETE CASCADE,
    path text NOT NULL CHECK (path IS NFC NORMALIZED),
    type text NOT NULL CHECK (type IN ('person', 'organization', 'project', 'program', 'concept', 'idea',
                                       'source', 'meeting', 'writing')),
    title text NOT NULL,
    dated date,
    frontmatter jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (NOT (frontmatter ?| ARRAY['status', 'deadline', 'due', 'due_date'])),
    compiled_truth text NOT NULL,
    content_sha256 text NOT NULL,
    UNIQUE (root_id, path)
);

-- Names a link may use for a page: its slug, title, frontmatter aliases, and those of pages merged into it.
-- alias_key is the NFC, case-folded, space-collapsed form; one key can name two pages (then it resolves to none).
CREATE TABLE IF NOT EXISTS brain.aliases (
    alias_key text NOT NULL,
    slug text NOT NULL REFERENCES brain.pages (slug) ON DELETE CASCADE ON UPDATE CASCADE,
    alias text NOT NULL,
    origin text NOT NULL CHECK (origin IN ('slug', 'title', 'frontmatter', 'merge')),
    PRIMARY KEY (alias_key, slug)
);
CREATE INDEX IF NOT EXISTS aliases_slug ON brain.aliases (slug);

-- Typed edges. Links found in page text are type 'mentions' with open validity; source_ref names the page file
-- the link was written in. A later relation closes by setting valid_until, never by deleting the row.
CREATE TABLE IF NOT EXISTS brain.edges (
    from_slug text NOT NULL REFERENCES brain.pages (slug) ON DELETE CASCADE ON UPDATE CASCADE,
    to_slug text NOT NULL REFERENCES brain.pages (slug) ON DELETE CASCADE ON UPDATE CASCADE,
    type text NOT NULL DEFAULT 'mentions',
    source_ref text NOT NULL,
    valid_from date,
    valid_until date,
    PRIMARY KEY (from_slug, to_slug, type, source_ref),
    CHECK (from_slug <> to_slug),
    CHECK (valid_until IS NULL OR valid_from IS NULL OR valid_until >= valid_from)
);
CREATE INDEX IF NOT EXISTS edges_to ON brain.edges (to_slug);

-- The append-only timeline below a page's divider, one row per entry, keyed by where it is written.
-- grade (lina#439 section 1) defaults to the least trusted one until a later issue gives it meaning.
CREATE TABLE IF NOT EXISTS brain.timeline (
    entry_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    page_slug text NOT NULL REFERENCES brain.pages (slug) ON DELETE CASCADE ON UPDATE CASCADE,
    root_id bigint NOT NULL REFERENCES brain.roots (root_id) ON DELETE CASCADE,
    path text NOT NULL,
    line int NOT NULL,
    entry_date date,
    grade text NOT NULL DEFAULT 'external' CHECK (grade IN ('owner', 'agent', 'external')),
    source_ref text,
    text text NOT NULL,
    UNIQUE (root_id, path, line)
);
CREATE INDEX IF NOT EXISTS timeline_page ON brain.timeline (page_slug, entry_date);

-- Short claims a later cycle extracts from a page. Not derivable from the files, so a reindex keeps them: the
-- deferred key lets reindex delete and re-insert their page in one transaction.
CREATE TABLE IF NOT EXISTS brain.atoms (
    atom_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    page_slug text NOT NULL REFERENCES brain.pages (slug) ON UPDATE CASCADE DEFERRABLE INITIALLY DEFERRED,
    claim text NOT NULL,
    grade text NOT NULL DEFAULT 'external' CHECK (grade IN ('owner', 'agent', 'external')),
    source_ref text,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS atoms_page ON brain.atoms (page_slug);

-- A recorded merge: from_slug's page folds into into_slug's. Its aliases carry over and edges re-point; the
-- file of from_slug stays where it is. Every import and reindex replays these in merged_at order.
CREATE TABLE IF NOT EXISTS brain.merges (
    from_slug text PRIMARY KEY,
    into_slug text NOT NULL,
    root_id bigint NOT NULL REFERENCES brain.roots (root_id) ON DELETE CASCADE,
    reason text,
    merged_at timestamptz NOT NULL DEFAULT now(),
    CHECK (from_slug <> into_slug)
);

-- A change an agent's cycle suggests (compiled-truth rewrite, merge, closing an edge, promotion); the owner or a
-- reviewer accepts it. Agent-scoped: the event trigger above gives it lina_agent_rows.
CREATE TABLE IF NOT EXISTS brain.proposals (
    proposal_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    agent_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('compiled_truth', 'merge', 'close_edge', 'promotion', 'timeline')),
    page_slug text,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    state text NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'accepted', 'rejected')),
    created_at timestamptz NOT NULL DEFAULT now(),
    decided_at timestamptz
);
CREATE INDEX IF NOT EXISTS proposals_open ON brain.proposals (agent_id, state);

GRANT USAGE ON SCHEMA brain TO lina_app;
GRANT SELECT ON brain.roots, brain.pages, brain.aliases, brain.edges, brain.timeline, brain.atoms, brain.merges
    TO lina_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON brain.proposals TO lina_app;

-- Page vectors in vec.embeddings: kind 'page', item_id = brain.pages.page_id, owner '*' like the shared external
-- message vectors (0009), so no agent's lina_agent_rows can write one and this SELECT-only policy lets every
-- agent read them.
ALTER TABLE vec.embeddings DROP CONSTRAINT IF EXISTS embeddings_kind_check;
ALTER TABLE vec.embeddings ADD CONSTRAINT embeddings_kind_check
    CHECK (kind IN ('message', 'chunk', 'summary', 'document', 'page'));
DO $$ BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'vec.embeddings'::regclass AND polname = 'lina_shared_page_vectors') THEN
        CREATE POLICY lina_shared_page_vectors ON vec.embeddings
            FOR SELECT TO lina_app USING (kind = 'page' AND agent_id = '*');
    END IF;
END $$;

-- A page's vector describes its text: it goes when the page goes or its file changes.
CREATE OR REPLACE FUNCTION brain.drop_page_vector() RETURNS trigger
    LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM vec.embeddings WHERE kind = 'page' AND item_id = OLD.page_id;
    RETURN NULL;
END $$;
DROP TRIGGER IF EXISTS pages_drop_vector ON brain.pages;
CREATE TRIGGER pages_drop_vector AFTER DELETE ON brain.pages
    FOR EACH ROW EXECUTE FUNCTION brain.drop_page_vector();
DROP TRIGGER IF EXISTS pages_drop_stale_vector ON brain.pages;
CREATE TRIGGER pages_drop_stale_vector AFTER UPDATE ON brain.pages
    FOR EACH ROW WHEN (OLD.content_sha256 IS DISTINCT FROM NEW.content_sha256 OR OLD.page_id <> NEW.page_id)
    EXECUTE FUNCTION brain.drop_page_vector();
