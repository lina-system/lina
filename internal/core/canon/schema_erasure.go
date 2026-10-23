// SPDX-License-Identifier: Apache-2.0
package canon

// EventErasureSchema leaves the ledger append-only except for the one rewrite a
// permanent deletion makes: once the person's removal erased an event, its text
// and material reference clear and the spans that indexed that text go with it,
// while the event's identity, origin and relations stay as the tombstone. Any
// other update or span deletion still aborts.
const EventErasureSchema = `
DROP TRIGGER conv_event_no_update;
CREATE TRIGGER conv_event_no_update BEFORE UPDATE ON conv_event
WHEN NOT (
 EXISTS(SELECT 1 FROM retract_target WHERE kind='event' AND id=OLD.id AND state='erased')
 AND NEW.id=OLD.id AND NEW.conversation_id=OLD.conversation_id
 AND NEW.parent_id IS OLD.parent_id AND NEW.person_id IS OLD.person_id
 AND NEW.agent_id=OLD.agent_id AND NEW.speaker_kind=OLD.speaker_kind
 AND NEW.surface=OLD.surface AND NEW.scaffold_kind IS OLD.scaffold_kind
 AND NEW.scaffold_id IS OLD.scaffold_id AND NEW.occurred_at=OLD.occurred_at
 AND NEW.recorded_at=OLD.recorded_at AND NEW.time_quality=OLD.time_quality
 AND NEW.origin=OLD.origin AND NEW.replay_of IS OLD.replay_of
 AND NEW.body='' AND NEW.material_id IS NULL AND NEW.material_revision IS NULL)
BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
DROP TRIGGER ev_span_no_delete;
CREATE TRIGGER ev_span_no_delete BEFORE DELETE ON ev_span
WHEN NOT EXISTS(SELECT 1 FROM retract_target WHERE kind='event' AND id=OLD.event_id AND state='erased')
BEGIN SELECT RAISE(ABORT,'ledger_append_only'); END;
`
