-- SPDX-License-Identifier: Apache-2.0
-- Synthetic fixture reconstructed from verified column metadata.
-- This is not an install dump; source triggers and secondary indexes are omitted.
PRAGMA user_version=0;
PRAGMA wal_autocheckpoint=0;
CREATE TABLE "async_delegations" (
    "delegation_id" TEXT,
    "origin_session" TEXT NOT NULL,
    "origin_ui_session_id" TEXT NOT NULL DEFAULT (''),
    "parent_session_id" TEXT,
    "state" TEXT NOT NULL,
    "dispatched_at" REAL NOT NULL,
    "completed_at" REAL,
    "updated_at" REAL NOT NULL,
    "event_json" TEXT,
    "result_json" TEXT,
    "delivery_state" TEXT NOT NULL DEFAULT ('pending'),
    "delivery_attempts" INTEGER NOT NULL DEFAULT (0),
    "delivered_at" REAL,
    "owner_pid" INTEGER,
    "owner_started_at" INTEGER,
    "task_json" TEXT,
    "delivery_claim" TEXT,
    "delivery_claimed_at" REAL,
    "origin_session_id" TEXT NOT NULL DEFAULT (''),
    PRIMARY KEY ("delegation_id")
);
CREATE TABLE "compression_locks" (
    "session_id" TEXT,
    "holder" TEXT NOT NULL,
    "acquired_at" REAL NOT NULL,
    "expires_at" REAL NOT NULL,
    PRIMARY KEY ("session_id")
);
CREATE TABLE "conversation_generations" (
    "source" TEXT NOT NULL,
    "session_key" TEXT NOT NULL,
    "generation" INTEGER NOT NULL DEFAULT (0),
    PRIMARY KEY ("source", "session_key")
);
CREATE TABLE "gateway_heartbeats" (
    "backend_id" TEXT,
    "pid" INTEGER NOT NULL,
    "started_at" REAL NOT NULL,
    "last_heartbeat" REAL NOT NULL,
    "profile" TEXT NOT NULL DEFAULT (''),
    "host" TEXT NOT NULL DEFAULT (''),
    PRIMARY KEY ("backend_id")
);
CREATE TABLE "gateway_hygiene_state" (
    "session_key" TEXT,
    "failure_streak" INTEGER NOT NULL DEFAULT (0),
    PRIMARY KEY ("session_key")
);
CREATE TABLE "gateway_routing" (
    "scope" TEXT NOT NULL DEFAULT (''),
    "session_key" TEXT NOT NULL,
    "entry_json" TEXT NOT NULL,
    "updated_at" REAL NOT NULL,
    PRIMARY KEY ("scope", "session_key")
);
CREATE TABLE "messages" (
    "id" INTEGER,
    "session_id" TEXT NOT NULL,
    "role" TEXT NOT NULL,
    "content" TEXT,
    "tool_call_id" TEXT,
    "tool_calls" TEXT,
    "tool_name" TEXT,
    "effect_disposition" TEXT,
    "timestamp" REAL NOT NULL,
    "token_count" INTEGER,
    "finish_reason" TEXT,
    "reasoning" TEXT,
    "reasoning_content" TEXT,
    "reasoning_details" TEXT,
    "codex_reasoning_items" TEXT,
    "codex_message_items" TEXT,
    "platform_message_id" TEXT,
    "observed" INTEGER DEFAULT (0),
    "_compressed_summary" INTEGER NOT NULL DEFAULT (0),
    "active" INTEGER NOT NULL DEFAULT (1),
    "compacted" INTEGER NOT NULL DEFAULT (0),
    "api_content" TEXT,
    "display_kind" TEXT,
    "display_metadata" TEXT,
    "display_identity" BLOB,
    "display_order" INTEGER,
    "message_uid" TEXT,
    "absorbed_message_uids" TEXT,
    "tool_call_uids" TEXT,
    "tool_call_uid" TEXT,
    PRIMARY KEY ("id")
);
CREATE TABLE "schema_version" (
    "version" INTEGER NOT NULL
);
CREATE TABLE "session_model_usage" (
    "session_id" TEXT NOT NULL,
    "model" TEXT NOT NULL,
    "billing_provider" TEXT NOT NULL DEFAULT (''),
    "billing_base_url" TEXT NOT NULL DEFAULT (''),
    "billing_mode" TEXT NOT NULL DEFAULT (''),
    "task" TEXT NOT NULL DEFAULT (''),
    "api_call_count" INTEGER NOT NULL DEFAULT (0),
    "input_tokens" INTEGER NOT NULL DEFAULT (0),
    "output_tokens" INTEGER NOT NULL DEFAULT (0),
    "cache_read_tokens" INTEGER NOT NULL DEFAULT (0),
    "cache_write_tokens" INTEGER NOT NULL DEFAULT (0),
    "reasoning_tokens" INTEGER NOT NULL DEFAULT (0),
    "estimated_cost_usd" REAL NOT NULL DEFAULT (0),
    "actual_cost_usd" REAL NOT NULL DEFAULT (0),
    "cost_status" TEXT,
    "cost_source" TEXT,
    "first_seen" REAL,
    "last_seen" REAL,
    PRIMARY KEY ("session_id", "model", "billing_provider", "billing_base_url", "billing_mode", "task")
);
CREATE TABLE "session_turn_leases" (
    "conversation_id" TEXT,
    "holder" TEXT NOT NULL,
    "acquired_at" REAL NOT NULL,
    "expires_at" REAL NOT NULL,
    PRIMARY KEY ("conversation_id")
);
CREATE TABLE "sessions" (
    "id" TEXT,
    "source" TEXT NOT NULL,
    "created_source" TEXT,
    "user_id" TEXT,
    "session_key" TEXT,
    "chat_id" TEXT,
    "chat_type" TEXT,
    "thread_id" TEXT,
    "display_name" TEXT,
    "origin_json" TEXT,
    "expiry_finalized" INTEGER DEFAULT (0),
    "model" TEXT,
    "model_config" TEXT,
    "system_prompt" TEXT,
    "system_prompt_hash" TEXT,
    "parent_session_id" TEXT,
    "started_at" REAL NOT NULL,
    "ended_at" REAL,
    "end_reason" TEXT,
    "message_count" INTEGER DEFAULT (0),
    "tool_call_count" INTEGER DEFAULT (0),
    "input_tokens" INTEGER DEFAULT (0),
    "output_tokens" INTEGER DEFAULT (0),
    "cache_read_tokens" INTEGER DEFAULT (0),
    "cache_write_tokens" INTEGER DEFAULT (0),
    "reasoning_tokens" INTEGER DEFAULT (0),
    "cwd" TEXT,
    "git_branch" TEXT,
    "git_repo_root" TEXT,
    "git_metadata_generation" INTEGER NOT NULL DEFAULT (0),
    "billing_provider" TEXT,
    "billing_base_url" TEXT,
    "billing_mode" TEXT,
    "estimated_cost_usd" REAL,
    "actual_cost_usd" REAL,
    "cost_status" TEXT,
    "cost_source" TEXT,
    "pricing_version" TEXT,
    "title" TEXT,
    "title_source" TEXT,
    "last_activity_at" REAL,
    "last_activity_description" TEXT,
    "last_activity_provenance" TEXT,
    "api_call_count" INTEGER DEFAULT (0),
    "handoff_state" TEXT,
    "handoff_platform" TEXT,
    "handoff_error" TEXT,
    "compression_failure_cooldown_until" REAL,
    "compression_failure_error" TEXT,
    "compression_fallback_streak" INTEGER NOT NULL DEFAULT (0),
    "compression_ineffective_count" INTEGER NOT NULL DEFAULT (0),
    "compression_recovery_deadline" REAL,
    "compression_overload_streak" INTEGER NOT NULL DEFAULT (0),
    "profile_name" TEXT,
    "transport_profile" TEXT,
    "rewind_count" INTEGER NOT NULL DEFAULT (0),
    "archived" INTEGER NOT NULL DEFAULT (0),
    "auto_archived" INTEGER NOT NULL DEFAULT (0),
    "pinned" INTEGER NOT NULL DEFAULT (0),
    "hidden" INTEGER NOT NULL DEFAULT (0),
    "last_read_at" REAL,
    "tool_names" TEXT,
    PRIMARY KEY ("id")
);
CREATE TABLE "state_meta" (
    "key" TEXT,
    "value" TEXT,
    PRIMARY KEY ("key")
);
CREATE TABLE "system_prompts" (
    "hash" TEXT,
    "prompt" TEXT NOT NULL,
    PRIMARY KEY ("hash")
);
CREATE VIEW messages_fts_src AS SELECT id,
    CASE WHEN role = 'tool' THEN substr(COALESCE(content, ''), 1, 8192) ELSE content END AS content,
    tool_name, tool_calls FROM messages;
CREATE VIEW messages_fts_trigram_src AS SELECT m.id, m.role, m.content, m.tool_name
    FROM messages AS m JOIN sessions AS s ON s.id = m.session_id
    WHERE m.role <> 'tool' AND s.source NOT IN ('cron', 'subagent')
    AND json_extract(CASE WHEN json_valid(s.model_config) THEN s.model_config ELSE json_object() END, '$._delegate_from') IS NULL;
CREATE VIRTUAL TABLE messages_fts USING fts5(content, tool_name, tool_calls, content='messages_fts_src', content_rowid='id');
CREATE VIRTUAL TABLE messages_fts_trigram USING fts5(content, tool_name, content='messages_fts_trigram_src', content_rowid='id', tokenize='trigram');
-- Synthetic rows only; no value for the unobserved schema_version row is guessed.
INSERT INTO sessions(id,source,started_at,model_config) VALUES ('synthetic-session','cli',100,'{}');
INSERT INTO messages(id,session_id,role,content,timestamp,message_uid,platform_message_id)
    VALUES (1,'synthetic-session','user','Synthetic request',100,'synthetic-message-1','synthetic-platform-1');
INSERT INTO messages(id,session_id,role,content,timestamp,message_uid,reasoning,finish_reason,codex_reasoning_items)
    VALUES (2,'synthetic-session','assistant','Synthetic response',101,'synthetic-message-2','synthetic reasoning','stop','[{"id":"synthetic-reasoning"}]');
INSERT INTO messages_fts(rowid,content,tool_name,tool_calls) SELECT id,content,tool_name,tool_calls FROM messages_fts_src;
INSERT INTO messages_fts_trigram(rowid,content,tool_name) SELECT id,content,tool_name FROM messages_fts_trigram_src;
