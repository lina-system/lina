// SPDX-License-Identifier: Apache-2.0
package canon

// evidenceCitationColumns is shared by the historical fill and live writers.
// Legacy quotes contain the resolved event's derived text, including when its
// body is a material. Offsets follow ev_span's UTF-8 byte convention.
const evidenceCitationColumns = `
 CASE json_extract(p.evidence,'$.kind') WHEN 'item' THEN 'memory' ELSE json_extract(p.evidence,'$.kind') END AS target_kind,
 json_extract(p.evidence,'$.id') AS target_id,
 CAST(coalesce(json_extract(p.evidence,'$.revision'),0) AS TEXT) AS target_revision,
 CASE WHEN json_extract(p.evidence,'$.kind')='event' AND length(json_extract(p.evidence,'$.quote'))>0 THEN 0 END AS span_start,
 CASE WHEN json_extract(p.evidence,'$.kind')='event' AND length(json_extract(p.evidence,'$.quote'))>0
 THEN length(CAST(json_extract(p.evidence,'$.quote') AS BLOB)) END AS span_end,
 coalesce(nullif(json_extract(p.evidence,'$.precision'),''),'none') AS precision,
 'cites' AS role`

const summaryCitationSelect = `
 SELECT 'summary' AS view_kind,n.id AS view_id,n.revision,
 e.ordinal,'summary' AS target_kind,e.child_id AS target_id,CAST(c.revision AS TEXT) AS target_revision,
 NULL AS span_start,NULL AS span_end,'none' AS precision,'cites' AS role
 FROM sum_node n JOIN sum_edge e ON e.parent_id=n.id JOIN sum_node c ON c.id=e.child_id
 UNION ALL
 SELECT 'summary',n.id,n.revision,c.ordinal+(SELECT count(*) FROM sum_edge e WHERE e.parent_id=n.id),
 'event',c.event_id,'0',NULL,NULL,'event',CASE c.reason WHEN '' THEN 'covers' ELSE c.reason END
 FROM sum_node n JOIN sum_cover c ON c.node_id=n.id`

const citeSchema = `
CREATE TABLE cite (
 view_kind TEXT NOT NULL CHECK(view_kind IN ('summary','memory','person_brief','record')),
 view_id TEXT NOT NULL CHECK(length(view_id)>0),
 added_revision TEXT NOT NULL CHECK(length(added_revision)>0),
 dropped_revision TEXT CHECK(dropped_revision IS NULL OR length(dropped_revision)>0),
 ordinal INTEGER NOT NULL CHECK(ordinal>=0),
 target_kind TEXT NOT NULL CHECK(target_kind IN ('event','summary','memory','material','work_result','external')),
 target_id TEXT NOT NULL CHECK(length(target_id)>0),
 target_revision TEXT NOT NULL,
 span_start INTEGER, span_end INTEGER,
 precision TEXT NOT NULL CHECK(precision IN ('event','session','none')),
 role TEXT NOT NULL CHECK(role IN ('cites','covers','replay','scaffold','summary_echo','retracted')),
 CHECK((span_start IS NULL AND span_end IS NULL) OR (span_start IS NOT NULL AND span_end IS NOT NULL AND span_start>=0 AND span_end>=span_start)),
 PRIMARY KEY(view_kind,view_id,ordinal,added_revision)
);
CREATE INDEX cite_target ON cite(target_kind,target_id);
CREATE UNIQUE INDEX cite_current_position ON cite(view_kind,view_id,ordinal) WHERE dropped_revision IS NULL;
CREATE UNIQUE INDEX cite_summary_event ON cite(view_id,target_id) WHERE view_kind='summary' AND target_kind='event' AND dropped_revision IS NULL;

-- Some older composed schemas installed briefs outside the application catalog.
CREATE TABLE IF NOT EXISTS memory_person_brief (
 person TEXT NOT NULL, agent TEXT NOT NULL, generation INTEGER NOT NULL,
 record BLOB NOT NULL CHECK(json_valid(record)), PRIMARY KEY(person,agent)
);

INSERT INTO cite(view_kind,view_id,added_revision,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role)
 SELECT view_kind,view_id,revision,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role
 FROM (` + summaryCitationSelect + `);

-- Include current items from old stores even if their revision journal is absent.
CREATE TEMP TABLE cite_steps AS
 WITH snapshots AS (
 SELECT item_id AS id,revision,record FROM memory_revision
 UNION ALL
 SELECT m.id,coalesce(json_extract(m.record,'$.revision'),1),m.record FROM memory_item m
 WHERE NOT EXISTS(SELECT 1 FROM memory_revision r WHERE r.item_id=m.id AND r.revision=coalesce(json_extract(m.record,'$.revision'),1))
 )
 SELECT 'memory' AS view_kind,id AS view_id,revision,record,
 row_number() OVER(PARTITION BY id ORDER BY revision) AS step,
 lead(revision) OVER(PARTITION BY id ORDER BY revision) AS next_revision FROM snapshots;
CREATE UNIQUE INDEX temp.cite_step_key ON cite_steps(view_kind,view_id,step);
INSERT INTO cite_steps
 SELECT 'person_brief',json_array(person,agent),generation,record,1,NULL FROM memory_person_brief;

CREATE TEMP TABLE cite_old AS
 WITH pointers AS (
 SELECT s.view_kind,s.view_id,s.revision,s.step,e.key AS ordinal,e.value AS evidence
 FROM cite_steps s,json_each(s.record,'$.evidence') e WHERE s.view_kind='memory' AND e.type='object'
 UNION ALL
 SELECT s.view_kind,s.view_id,s.revision,s.step,
 row_number() OVER(PARTITION BY s.view_id ORDER BY sentence.key,e.key)-1,e.value
 FROM cite_steps s,json_each(s.record,'$.sentences') sentence,json_each(sentence.value,'$.citations') e
 WHERE s.view_kind='person_brief' AND e.type='object'
 )
 SELECT p.view_kind,p.view_id,p.revision,p.step,p.ordinal,` + evidenceCitationColumns + ` FROM pointers p;

-- Consecutive snapshots of the same pointer form one half-open interval.
INSERT INTO cite(view_kind,view_id,added_revision,dropped_revision,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role)
 WITH runs AS (
 SELECT *,step-row_number() OVER(PARTITION BY view_kind,view_id,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role ORDER BY step) AS island
 FROM cite_old
 ), intervals AS (
 SELECT view_kind,view_id,min(revision) AS added_revision,max(step) AS last_step,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role
 FROM runs GROUP BY view_kind,view_id,ordinal,target_kind,target_id,target_revision,span_start,span_end,precision,role,island
 )
 SELECT i.view_kind,i.view_id,i.added_revision,s.next_revision,i.ordinal,i.target_kind,i.target_id,i.target_revision,i.span_start,i.span_end,i.precision,i.role
 FROM intervals i JOIN cite_steps s ON s.view_kind=i.view_kind AND s.view_id=i.view_id AND s.step=i.last_step;
DROP TABLE cite_old;
DROP TABLE cite_steps;

-- Files are canon for records; force their normal startup projection to refill.
UPDATE records_index_state SET head='' WHERE singleton=1;
CREATE TRIGGER cite_memory_delete AFTER DELETE ON memory_item BEGIN
 DELETE FROM cite WHERE view_kind='memory' AND view_id=OLD.id;
END;
CREATE TRIGGER cite_record_delete AFTER DELETE ON records_index BEGIN
 DELETE FROM cite WHERE view_kind='record' AND view_id=OLD.id;
END;
`
