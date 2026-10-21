-- SPDX-License-Identifier: Apache-2.0

CREATE TABLE turn_control (
 turn_id TEXT PRIMARY KEY NOT NULL REFERENCES turn_runs(id),
 input_event TEXT NOT NULL REFERENCES conv_event(id),
 issuer TEXT,
 notice_event TEXT UNIQUE REFERENCES conv_event(id)
);
