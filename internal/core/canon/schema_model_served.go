// SPDX-License-Identifier: Apache-2.0

package canon

// ModelServedSchema records which model served a short task.
// The row stores no task text.
const ModelServedSchema = `
CREATE TABLE model_served (
 id TEXT PRIMARY KEY NOT NULL CHECK(length(id)=32),
 kind TEXT NOT NULL CHECK(length(kind)>0),
 provider TEXT NOT NULL CHECK(length(provider)>0),
 model TEXT NOT NULL CHECK(length(model)>0),
 route TEXT NOT NULL CHECK(route IN ('local','main')),
 reason TEXT NOT NULL CHECK(length(reason)>0),
 digest TEXT NOT NULL CHECK(length(digest)=64),
 input_tokens INTEGER CHECK(input_tokens IS NULL OR input_tokens>=0),
 output_tokens INTEGER CHECK(output_tokens IS NULL OR output_tokens>=0),
 created_at TEXT NOT NULL CHECK(length(created_at)>0)
);
CREATE TRIGGER model_served_no_update BEFORE UPDATE ON model_served BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
CREATE TRIGGER model_served_no_delete BEFORE DELETE ON model_served BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
`
