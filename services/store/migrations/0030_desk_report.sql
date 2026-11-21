-- A Desk registration follows a relative path. A file identity locates a rename
-- within the mounted Desk. Keep withdrawn versions for history but unpublish them.
ALTER TABLE reports.report
    ADD COLUMN desk_path text UNIQUE,
    ADD COLUMN desk_file_id text,
    ADD COLUMN desk_state text CHECK (desk_state IN ('active', 'gone', 'denied')),
    ADD CONSTRAINT desk_registration CHECK (
        (desk_path IS NULL AND desk_file_id IS NULL AND desk_state IS NULL) OR
        (desk_path IS NOT NULL AND desk_file_id IS NOT NULL AND desk_state IS NOT NULL)
    );
