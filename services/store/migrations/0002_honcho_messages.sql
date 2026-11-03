-- Honcho is a trusted cross-agent processor, not an agent-scoped login.
-- Text remains immutable: this role cannot update or delete shared rows.
GRANT USAGE ON SCHEMA memory TO honcho;
GRANT SELECT, INSERT ON memory.messages TO honcho;
GRANT REFERENCES (message_id) ON memory.messages TO honcho;

CREATE POLICY honcho_messages_select ON memory.messages
    FOR SELECT TO honcho USING (true);
CREATE POLICY honcho_messages_insert ON memory.messages
    FOR INSERT TO honcho WITH CHECK (agent_id IS NOT NULL);
