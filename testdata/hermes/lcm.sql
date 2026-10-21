-- SPDX-License-Identifier: Apache-2.0
-- Synthetic fixture reconstructed from verified column metadata.
-- This is not an install dump; source triggers and secondary indexes are omitted.
PRAGMA user_version=0;
PRAGMA wal_autocheckpoint=0;
CREATE TABLE "host_uid_bindings" (
    "store_id" INTEGER NOT NULL,
    "uid" TEXT NOT NULL,
    "lineage_key" TEXT NOT NULL,
    "kind" TEXT NOT NULL,
    "binding_version" INTEGER NOT NULL,
    "proof_kind" TEXT,
    "created_at" REAL,
    "first_check" TEXT,
    "disagree_seen" INTEGER NOT NULL DEFAULT (0)
);
CREATE TABLE "lcm_chunk_binary" (
    "chunk_id" TEXT,
    "identity_hash" TEXT,
    "bits" BLOB NOT NULL,
    PRIMARY KEY ("chunk_id", "identity_hash")
);
CREATE TABLE "lcm_chunk_meta" (
    "chunk_id" TEXT,
    "identity_hash" TEXT,
    "store_id" INTEGER,
    "chunk_index" INTEGER,
    "char_start" INTEGER,
    "char_end" INTEGER,
    "token_estimate" INTEGER,
    "embedded_at" TEXT,
    "archived" INTEGER DEFAULT (0),
    PRIMARY KEY ("chunk_id", "identity_hash")
);
CREATE TABLE "lcm_chunk_vectors" (
    "chunk_id" TEXT,
    "identity_hash" TEXT,
    "vec" BLOB NOT NULL,
    PRIMARY KEY ("chunk_id", "identity_hash")
);
CREATE TABLE "lcm_embedding_backfill_inflight" (
    "embedded_id" TEXT,
    "identity_hash" TEXT,
    "lease_id" TEXT,
    "generation" INTEGER,
    "claimed_at" REAL,
    "state" TEXT NOT NULL DEFAULT ('claimed'),
    "request_id" TEXT,
    "updated_at" REAL,
    "last_error" TEXT,
    PRIMARY KEY ("embedded_id", "identity_hash")
);
CREATE TABLE "lcm_embedding_binary" (
    "embedded_id" TEXT,
    "identity_hash" TEXT,
    "bits" BLOB NOT NULL,
    PRIMARY KEY ("embedded_id", "identity_hash")
);
CREATE TABLE "lcm_embedding_meta" (
    "embedded_id" TEXT,
    "embedded_kind" TEXT,
    "identity_hash" TEXT,
    "embedded_at" TEXT,
    "source_token_count" INTEGER,
    "archived" INTEGER DEFAULT (0),
    PRIMARY KEY ("embedded_id", "embedded_kind", "identity_hash")
);
CREATE TABLE "lcm_embedding_profile" (
    "identity_hash" TEXT,
    "provider" TEXT NOT NULL,
    "model_name" TEXT NOT NULL,
    "revision" TEXT NOT NULL DEFAULT (''),
    "dim" INTEGER,
    "dtype" TEXT NOT NULL DEFAULT ('float32'),
    "byteorder" TEXT NOT NULL DEFAULT ('little'),
    "task" TEXT NOT NULL DEFAULT ('summary'),
    "registered_at" TEXT,
    "active" INTEGER DEFAULT (1),
    "archived_at" TEXT,
    "data_version" INTEGER NOT NULL DEFAULT (0),
    PRIMARY KEY ("identity_hash")
);
CREATE TABLE "lcm_embedding_vectors" (
    "embedded_id" TEXT,
    "identity_hash" TEXT,
    "vec" BLOB NOT NULL,
    PRIMARY KEY ("embedded_id", "identity_hash")
);
CREATE TABLE "lcm_lifecycle_state" (
    "conversation_id" TEXT,
    "current_session_id" TEXT,
    "last_finalized_session_id" TEXT,
    "current_frontier_store_id" INTEGER NOT NULL DEFAULT (0),
    "last_finalized_frontier_store_id" INTEGER NOT NULL DEFAULT (0),
    "debt_kind" TEXT,
    "debt_size_estimate" INTEGER NOT NULL DEFAULT (0),
    "current_bound_at" REAL,
    "last_finalized_at" REAL,
    "debt_updated_at" REAL,
    "last_maintenance_attempt_at" REAL,
    "last_rollover_at" REAL,
    "last_reset_at" REAL,
    "updated_at" REAL NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY ("conversation_id")
);
CREATE TABLE "lcm_migration_state" (
    "step_name" TEXT,
    "completed_at" REAL NOT NULL,
    PRIMARY KEY ("step_name")
);
CREATE TABLE "lcm_rollup_invalidations" (
    "event_id" INTEGER,
    "node_id" INTEGER,
    "scope" TEXT NOT NULL,
    "covered_start" REAL NOT NULL,
    "covered_end" REAL NOT NULL,
    "next_day" TEXT,
    "operation" TEXT NOT NULL,
    "created_at" REAL NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY ("event_id")
);
CREATE TABLE "lcm_rollup_sources" (
    "rollup_id" INTEGER NOT NULL,
    "node_id" INTEGER NOT NULL,
    PRIMARY KEY ("rollup_id", "node_id")
);
CREATE TABLE "lcm_rollup_state" (
    "period_kind" TEXT NOT NULL,
    "scope" TEXT NOT NULL DEFAULT (''),
    "last_build_cursor" TEXT,
    "last_built_at" TEXT,
    PRIMARY KEY ("period_kind", "scope")
);
CREATE TABLE "lcm_rollups" (
    "rollup_id" INTEGER,
    "period_kind" TEXT NOT NULL,
    "period_start" TEXT NOT NULL,
    "scope" TEXT NOT NULL,
    "summary" TEXT,
    "token_count" INTEGER,
    "status" TEXT NOT NULL DEFAULT ('building'),
    "built_at" TEXT,
    "source_fingerprint" TEXT,
    "error" TEXT,
    "generation" INTEGER NOT NULL DEFAULT (0),
    "lease_expires_at" TEXT,
    "lease_nonce" TEXT NOT NULL DEFAULT (''),
    "failed_at" TEXT,
    PRIMARY KEY ("rollup_id")
);
CREATE TABLE "message_relations" (
    "relation_id" INTEGER,
    "store_id" INTEGER NOT NULL,
    "kind" TEXT NOT NULL,
    "related_store_id" INTEGER,
    "ordinal" INTEGER,
    "observed_at" REAL,
    "created_at" REAL,
    PRIMARY KEY ("relation_id")
);
CREATE TABLE "messages" (
    "store_id" INTEGER,
    "session_id" TEXT NOT NULL,
    "source" TEXT DEFAULT (''),
    "conversation_id" TEXT DEFAULT (''),
    "role" TEXT NOT NULL,
    "content" TEXT,
    "tool_call_id" TEXT,
    "tool_calls" TEXT,
    "tool_name" TEXT,
    "timestamp" REAL NOT NULL,
    "token_estimate" INTEGER DEFAULT (0),
    "pinned" INTEGER DEFAULT (0),
    "ingested_at" REAL,
    "observed_at" REAL,
    "observed_at_source" TEXT,
    PRIMARY KEY ("store_id")
);
CREATE TABLE "metadata" (
    "key" TEXT,
    "value" TEXT,
    PRIMARY KEY ("key")
);
CREATE TABLE "summary_node_provenance" (
    "node_id" INTEGER,
    "escalation_level" INTEGER NOT NULL,
    "model" TEXT NOT NULL DEFAULT (''),
    "created_at" REAL NOT NULL,
    PRIMARY KEY ("node_id")
);
CREATE TABLE "summary_nodes" (
    "node_id" INTEGER,
    "session_id" TEXT NOT NULL,
    "depth" INTEGER NOT NULL DEFAULT (0),
    "summary" TEXT NOT NULL,
    "token_count" INTEGER DEFAULT (0),
    "source_token_count" INTEGER DEFAULT (0),
    "source_ids" TEXT NOT NULL DEFAULT ('[]'),
    "source_type" TEXT NOT NULL DEFAULT ('messages'),
    "created_at" REAL NOT NULL,
    "earliest_at" REAL,
    "latest_at" REAL,
    "expand_hint" TEXT DEFAULT (''),
    PRIMARY KEY ("node_id")
);
CREATE VIRTUAL TABLE messages_fts USING fts5(content, content='messages', content_rowid='store_id');
CREATE VIRTUAL TABLE nodes_fts USING fts5(summary, content='summary_nodes', content_rowid='node_id');
CREATE VIRTUAL TABLE messages_fts_stem USING fts5(content, content='messages', content_rowid='store_id', tokenize='porter unicode61');
-- Synthetic rows only. Fixed ids and times make reconciliation deterministic.
INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp,observed_at,observed_at_source)
    VALUES (1,'synthetic-session','cli','synthetic-conversation','user','Synthetic request',100,100,'host');
INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,tool_calls,timestamp)
    VALUES (2,'synthetic-session','cli','synthetic-conversation','assistant','Synthetic response','[{"id":"synthetic-call","name":"synthetic-tool"}]',101);
INSERT INTO messages_fts(rowid,content) SELECT store_id,content FROM messages;
INSERT INTO messages_fts_stem(rowid,content) SELECT store_id,content FROM messages;
INSERT INTO summary_nodes(node_id,session_id,summary,source_ids,created_at)
    VALUES (1,'synthetic-session','Synthetic summary','[1,2]',102);
INSERT INTO nodes_fts(rowid,summary) SELECT node_id,summary FROM summary_nodes;
INSERT INTO summary_node_provenance(node_id,escalation_level,model,created_at)
    VALUES (1,0,'synthetic-model',102);
INSERT INTO host_uid_bindings(store_id,uid,lineage_key,kind,binding_version)
    VALUES (1,'synthetic-message-1','synthetic-lineage','canonical',1);
INSERT INTO message_relations(relation_id,store_id,kind,related_store_id)
    VALUES (1,2,'view_of',1);
INSERT INTO lcm_lifecycle_state(conversation_id,current_session_id,updated_at)
    VALUES ('synthetic-conversation','synthetic-session',103);
