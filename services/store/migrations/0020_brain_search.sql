-- The keyword index of brain pages for brain_search (lina#446): per page and analyzer, the Korean keyword terms of
-- its title, names and compiled truth (hermes_cli/brain/search.py). `kiwi` terms are Kiwi morphemes (the korean
-- extra), `bigram` terms character bigrams; a query reads the analyzer it can produce. Written only by
-- `lina brain search-index` as the store owner, like the rest of the brain; derived from the pages, so a page that
-- goes takes its terms with it, and a page whose file changed (content_sha256) is indexed again.
--
-- Shared like brain.pages (migration 0014): no agent_id, and lina_app may only SELECT. The page vectors of the
-- semantic leg are vec.embeddings rows of kind 'page' (0014); nothing here.

CREATE TABLE IF NOT EXISTS brain.search_docs (
    slug text NOT NULL REFERENCES brain.pages (slug) ON DELETE CASCADE ON UPDATE CASCADE,
    analyzer text NOT NULL CHECK (analyzer IN ('kiwi', 'bigram')),
    content_sha256 text NOT NULL,
    length real NOT NULL CHECK (length >= 0),
    PRIMARY KEY (slug, analyzer)
);

CREATE TABLE IF NOT EXISTS brain.search_terms (
    slug text NOT NULL,
    analyzer text NOT NULL,
    term text NOT NULL CHECK (term <> ''),
    tf real NOT NULL CHECK (tf > 0),
    PRIMARY KEY (analyzer, term, slug),
    FOREIGN KEY (slug, analyzer) REFERENCES brain.search_docs (slug, analyzer) ON DELETE CASCADE ON UPDATE CASCADE
);
CREATE INDEX IF NOT EXISTS search_terms_doc ON brain.search_terms (slug, analyzer);

GRANT SELECT ON brain.search_docs, brain.search_terms TO lina_app;
