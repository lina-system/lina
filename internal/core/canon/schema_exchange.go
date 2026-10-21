// SPDX-License-Identifier: Apache-2.0
package canon

// ExchangeSchema is part of the shared application migration catalog.
const ExchangeSchema = `
CREATE TABLE exchanges (
 id TEXT PRIMARY KEY NOT NULL,
 from_agent TEXT NOT NULL, to_agent TEXT NOT NULL CHECK(to_agent!=from_agent),
 delivery_id TEXT NOT NULL UNIQUE,
 round INTEGER NOT NULL CHECK(round BETWEEN 1 AND 4),
 outcome TEXT NOT NULL CHECK(outcome IN ('pending','acknowledged','checked','expired','round_limit')),
 needs_person INTEGER NOT NULL DEFAULT 0 CHECK(needs_person IN (0,1))
);
CREATE TABLE exch_msg (
 delivery_id TEXT PRIMARY KEY NOT NULL,
 exchange_id TEXT NOT NULL REFERENCES exchanges(id),
 round INTEGER NOT NULL,
 handoff BLOB NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('queued','delivered','read','answered')),
 inbox_id INTEGER UNIQUE REFERENCES inbox(id),
 answer BLOB,
 UNIQUE(exchange_id,round)
);
CREATE TABLE exch_event (
 id INTEGER PRIMARY KEY,
 exchange_id TEXT NOT NULL REFERENCES exchanges(id),
 delivery_id TEXT NOT NULL REFERENCES exch_msg(delivery_id),
 event_id TEXT NOT NULL UNIQUE REFERENCES conv_event(id),
 kind TEXT NOT NULL
);
CREATE TRIGGER exch_event_immutable_update BEFORE UPDATE ON exch_event BEGIN SELECT RAISE(ABORT,'immutable exchange event'); END;
CREATE TRIGGER exch_event_immutable_delete BEFORE DELETE ON exch_event BEGIN SELECT RAISE(ABORT,'immutable exchange event'); END;
CREATE TABLE exch_waiting (
 exchange_id TEXT PRIMARY KEY REFERENCES exchanges(id),
 person_id TEXT NOT NULL,
 question TEXT NOT NULL
);
CREATE INDEX exch_event_exchange ON exch_event(exchange_id,id);
`
