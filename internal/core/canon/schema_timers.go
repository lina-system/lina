// SPDX-License-Identifier: Apache-2.0
package canon

const TimersSchema = `
CREATE TABLE timer (
 id TEXT PRIMARY KEY,
 person TEXT NOT NULL,
 agent TEXT NOT NULL,
 conversation TEXT NOT NULL,
 authored BLOB NOT NULL,
 next_at TEXT NOT NULL,
 enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)),
 signature TEXT NOT NULL DEFAULT '',
 initialized INTEGER NOT NULL DEFAULT 0 CHECK(initialized IN (0,1))
);
CREATE INDEX timer_due ON timer(enabled,next_at);
CREATE TABLE timer_fire (
 id TEXT PRIMARY KEY,
 timer_id TEXT NOT NULL REFERENCES timer(id),
 instant TEXT NOT NULL,
 authored BLOB NOT NULL,
 read_only INTEGER NOT NULL CHECK(read_only IN (0,1)),
 state TEXT NOT NULL CHECK(state IN ('pending','claimed','handled','unknown','cancelled')),
 epoch TEXT NOT NULL DEFAULT '',
 attempts INTEGER NOT NULL DEFAULT 0,
 sampled INTEGER NOT NULL DEFAULT 0 CHECK(sampled IN (0,1)),
 changed INTEGER NOT NULL DEFAULT 0 CHECK(changed IN (0,1)),
 signature TEXT NOT NULL DEFAULT '',
 UNIQUE(timer_id,instant)
);
CREATE INDEX timer_fire_pending ON timer_fire(state,instant);
`
