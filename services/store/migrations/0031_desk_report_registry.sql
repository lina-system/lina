-- An explicit withdrawal in the retired index has a slug but no published body.
CREATE TABLE reports.desk_alias (
    slug text PRIMARY KEY,
    desk_path text NOT NULL UNIQUE,
    agent_id text NOT NULL CHECK (agent_id = 'desk:reports'),
    report_id text REFERENCES reports.report ON DELETE CASCADE,
    given_title text
);

-- Only the current page's approved images are served. The report id and
-- position provide a same-origin URL independent of any Desk filesystem name.
CREATE TABLE reports.desk_image (
    report_id text NOT NULL REFERENCES reports.report ON DELETE CASCADE,
    agent_id text NOT NULL CHECK (agent_id = 'desk:reports'),
    position smallint NOT NULL CHECK (position BETWEEN 0 AND 39),
    src text NOT NULL,
    mime text NOT NULL,
    digest text NOT NULL,
    data bytea NOT NULL CHECK (octet_length(data) <= 5242880),
    PRIMARY KEY (report_id, position),
    UNIQUE (report_id, src)
);

GRANT SELECT, INSERT ON reports.desk_alias TO lina_app;
GRANT SELECT, INSERT, DELETE ON reports.desk_image TO lina_app;
