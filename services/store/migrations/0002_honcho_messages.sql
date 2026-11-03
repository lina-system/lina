-- Honcho is a trusted cross-agent processor, not an agent-scoped login.
-- Text remains immutable: this role cannot update or delete shared rows.
GRANT USAGE ON SCHEMA memory TO honcho;
GRANT SELECT, INSERT ON memory.messages TO honcho;
GRANT REFERENCES (message_id) ON memory.messages TO honcho;

CREATE POLICY honcho_messages_select ON memory.messages
    FOR SELECT TO honcho USING (true);
CREATE POLICY honcho_messages_insert ON memory.messages
    FOR INSERT TO honcho WITH CHECK (agent_id IS NOT NULL);

-- Store migrations precede Honcho's job, so the metadata table may not exist.
-- Expose only a holder predicate, not SELECT access to Honcho's metadata.
CREATE FUNCTION memory.honcho_holds_message(shared_id bigint) RETURNS boolean
    LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS $$
BEGIN
    IF to_regclass('ltm.message_meta') IS NULL THEN
        RETURN false;
    END IF;
    RETURN EXISTS (SELECT 1 FROM ltm.message_meta WHERE message_id = shared_id);
END $$;
REVOKE ALL ON FUNCTION memory.honcho_holds_message(bigint) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION memory.honcho_holds_message(bigint) TO lina_app;
