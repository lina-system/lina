-- External messages read into brain timelines (lina#441, design lina#439 section 1): one row per message reference
-- `lina brain extract` has handled (hermes_cli/brain/extract.py, docs/brain.md "External messages"). A message whose
-- answer was valid is `written` (its line is on a page, `page_slug`) and never read again; one whose answer was not
-- is `invalid` and read again on a later run until it has failed `attempts` times. `reason` is the validator's own
-- message, never the message text or the model's answer.
--
-- Shared like brain.pages (migration 0014): no agent_id, written only by the store owner, and lina_app may only
-- SELECT.

CREATE TABLE IF NOT EXISTS brain.extracted (
    -- msg:messages:<uid>, as `lina messages` names the stored message (lina#549).
    source_ref text PRIMARY KEY CHECK (source_ref LIKE 'msg:messages:%'),
    outcome text NOT NULL CHECK (outcome IN ('written', 'invalid')),
    page_slug text,
    attempts integer NOT NULL DEFAULT 1 CHECK (attempts > 0),
    reason text,
    first_at timestamptz NOT NULL DEFAULT now(),
    last_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((outcome = 'written') = (page_slug IS NOT NULL))
);

GRANT SELECT ON brain.extracted TO lina_app;
