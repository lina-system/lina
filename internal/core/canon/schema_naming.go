// SPDX-License-Identifier: Apache-2.0
package canon

const namingSchema = `
CREATE TABLE naming_breaches (
 event_id TEXT PRIMARY KEY REFERENCES conv_event(id) ON DELETE CASCADE,
 person TEXT NOT NULL,
 agent TEXT NOT NULL
);
CREATE INDEX naming_breaches_agent ON naming_breaches(person,agent);
`
