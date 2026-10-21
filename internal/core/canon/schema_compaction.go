// SPDX-License-Identifier: Apache-2.0
package canon

const compactionSchema = `
CREATE TABLE sum_node (
 id TEXT PRIMARY KEY NOT NULL CHECK(length(id)>0),
 conversation_id TEXT NOT NULL CHECK(length(conversation_id)>0),
 level INTEGER NOT NULL CHECK(level>=0),
 model TEXT NOT NULL CHECK(length(model)>0),
 prompt_version TEXT NOT NULL CHECK(length(prompt_version)>0),
 built_at TEXT NOT NULL, start_at TEXT NOT NULL, end_at TEXT NOT NULL,
 epoch INTEGER NOT NULL CHECK(epoch>=0),
 status TEXT NOT NULL CHECK(status IN ('ready','invalid','degraded')),
 text TEXT NOT NULL, dropped TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0)
);
CREATE INDEX sum_conversation ON sum_node(conversation_id);
CREATE TABLE sum_edge (
 parent_id TEXT NOT NULL REFERENCES sum_node(id),
 child_id TEXT NOT NULL UNIQUE REFERENCES sum_node(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 CHECK(parent_id<>child_id),
 PRIMARY KEY(parent_id,ordinal)
);
CREATE TRIGGER sum_edge_tree BEFORE INSERT ON sum_edge
WHEN NOT EXISTS(SELECT 1 FROM sum_node p JOIN sum_node c
 ON p.conversation_id=c.conversation_id
 WHERE p.id=NEW.parent_id AND c.id=NEW.child_id AND p.level>c.level AND c.status='ready' AND p.epoch=c.epoch)
BEGIN SELECT RAISE(ABORT,'invalid_summary_child'); END;
CREATE TABLE sum_cover (
 node_id TEXT NOT NULL REFERENCES sum_node(id),
 event_id TEXT NOT NULL UNIQUE REFERENCES conv_event(id),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 reason TEXT NOT NULL CHECK(reason IN ('','replay','scaffold','summary_echo','retracted')),
 PRIMARY KEY(node_id,ordinal)
);
CREATE TRIGGER sum_cover_conversation BEFORE INSERT ON sum_cover
WHEN NOT EXISTS(SELECT 1 FROM sum_node n JOIN conv_event e ON e.conversation_id=n.conversation_id
 WHERE n.id=NEW.node_id AND e.id=NEW.event_id)
BEGIN SELECT RAISE(ABORT,'invalid_summary_event'); END;
CREATE VIEW sum_affected AS
WITH RECURSIVE affected(id) AS (
 SELECT c.node_id FROM sum_cover c WHERE c.reason='' AND NOT EXISTS(SELECT 1 FROM retract_visible_event e WHERE e.id=c.event_id)
 UNION
 SELECT e.parent_id FROM sum_edge e JOIN affected a ON a.id=e.child_id
)
SELECT id FROM affected;
CREATE TRIGGER sum_revoke AFTER UPDATE OF epoch ON retract_meta BEGIN
 UPDATE sum_node SET status='invalid',text='',dropped=''
 WHERE id IN (SELECT id FROM sum_affected);
END;
`
