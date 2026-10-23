-- SPDX-License-Identifier: Apache-2.0
-- Add to state.sql. Independently reconstructed from the referenced schema.
CREATE TABLE delivery_obligations (
 obligation_id TEXT PRIMARY KEY,
 session_key TEXT NOT NULL,
 platform TEXT NOT NULL,
 chat_id TEXT NOT NULL,
 thread_id TEXT,
 content TEXT NOT NULL,
 state TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 created_at REAL NOT NULL,
 updated_at REAL NOT NULL,
 owner_pid INTEGER,
 owner_started_at INTEGER,
 last_error TEXT,
 adapter_profile TEXT
);
INSERT INTO delivery_obligations(obligation_id,session_key,platform,chat_id,content,state,created_at,updated_at)
 VALUES('synthetic-obligation','synthetic-session','test','synthetic-chat','Synthetic delivery','pending',100,100);
