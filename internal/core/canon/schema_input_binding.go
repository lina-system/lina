// SPDX-License-Identifier: Apache-2.0
package canon

const inputBindingSchema = `
CREATE UNIQUE INDEX input_binding_parent ON conv_event(parent_id) WHERE scaffold_kind='input_binding';
CREATE TABLE records_activity (
 id TEXT PRIMARY KEY REFERENCES records_index(id) ON DELETE CASCADE,
 last_activity TEXT NOT NULL
);
UPDATE records_index_state SET head='unbuilt';
`
