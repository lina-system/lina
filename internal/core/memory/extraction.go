// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

var ErrJobConflict = errors.New("memory_job_conflict")

type Utterance struct {
	Conversation string     `json:"conversation"`
	Text         string     `json:"text"`
	Events       []Evidence `json:"events"`
}

// ExtractionBatch is prepared by Core from an ordered, bounded ledger snapshot.
// Through is the source cursor, independent of the job's task sequence. The
// scheduler persists it in the immutable task; Apply commits with its watermark.
type ExtractionBatch struct {
	Agent      string      `json:"agent"`
	Person     string      `json:"person"`
	After      int64       `json:"after"`
	Through    int64       `json:"through"`
	Revocation int64       `json:"revocation"`
	Utterances []Utterance `json:"utterances"`
}

// PrepareExtraction resolves only this agent's person events. Non-person turns,
// unavailable events and conversation changes break a burst. Resolve must read
// canonical, masked bodies and follow replay/view lineage, as it does for Insert.
// No utterances means no model task should be enqueued. Through still lets the
// scheduler skip an ineligible prefix on its next scan.
func (s *Store) PrepareExtraction(ctx context.Context, scope RecallScope, after int64, limit int, window time.Duration, resolve ResolveEvidence) (ExtractionBatch, error) {
	batch := ExtractionBatch{Agent: scope.Agent, Person: scope.Person, After: after, Through: after}
	if s == nil || s.db == nil || !present(scope.Agent) || scope.Agent == string(All) || !present(scope.Person) || after < 0 || limit < 1 || limit > 1000 || window < 0 || resolve == nil {
		return batch, ErrInvalid
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&batch.Revocation); err != nil {
			return err
		}
		scope.ExternalModel = true
		blocked, err := restrictedEvents(ctx, tx, scope)
		if err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT e.rowid,e.id,e.conversation_id,e.speaker_kind,coalesce(e.person_id,''),
   (EXISTS(SELECT 1 FROM retract_visible_event v WHERE v.id=e.id)
    AND json_extract(e.origin,'$.kind')='native' AND e.replay_of IS NULL AND NOT EXISTS(SELECT 1 FROM ev_rel r WHERE r.event_id=e.id AND r.kind='view_of'))
   FROM conv_event e WHERE e.agent_id=? AND e.rowid>? ORDER BY e.rowid LIMIT ?`, scope.Agent, after, limit)
		if err != nil {
			return err
		}
		type source struct {
			sequence                          int64
			id, conversation, speaker, person string
			visible                           bool
		}
		var sources []source
		for rows.Next() {
			var v source
			if err := rows.Scan(&v.sequence, &v.id, &v.conversation, &v.speaker, &v.person, &v.visible); err != nil {
				rows.Close()
				return err
			}
			sources = append(sources, v)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		seen := map[string]bool{}
		previous := ""
		var started, last int64
		for _, v := range sources {
			batch.Through = v.sequence
			if !v.visible || v.speaker != string(guard.Person) || v.person != scope.Person || blocked[v.id] {
				previous = ""
				continue
			}
			ref := Evidence{Kind: Event, ID: v.id, Precision: Exact}
			origin, err := resolve(ctx, tx, ref)
			if err != nil {
				return err
			}
			if origin.Speaker != guard.Person || origin.Person != scope.Person || origin.Agent != scope.Agent || origin.PlanEnded {
				return ErrPersonEvidence
			}
			if origin.ID != "" {
				ref.ID = origin.ID
			}
			if blocked[ref.ID] || origin.Scope == NeverLeaves || !present(origin.Quote) {
				previous = ""
				continue
			}
			if !validScope(origin.Scope) {
				return ErrScope
			}
			masked, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			if seen[ref.ID] {
				continue
			}
			seen[ref.ID] = true
			ref.SourceAgent = origin.Agent
			ref.OccurredAt = origin.OccurredAt
			ref.Quote = masked
			gap := int64(window / time.Second)
			if previous != v.conversation || origin.OccurredAt < last || origin.OccurredAt-started > gap {
				batch.Utterances = append(batch.Utterances, Utterance{Conversation: v.conversation})
				started = origin.OccurredAt
			}
			u := &batch.Utterances[len(batch.Utterances)-1]
			if u.Text != "" {
				u.Text += "\n"
			}
			u.Text += masked
			u.Events = append(u.Events, ref)
			previous = v.conversation
			last = origin.OccurredAt
		}
		return nil
	})
	return batch, err
}

// ApplyExtraction is called only inside Core's fenced job Apply transaction.
// The task supplies the allowed sources; model output never supplies authority.
func ApplyExtraction(ctx context.Context, tx *canon.Tx, batch ExtractionBatch, items []Item, resolve ResolveEvidence) error {
	if tx == nil || resolve == nil || !present(batch.Agent) || batch.Agent == string(All) || !present(batch.Person) || len(items) > 100 {
		return ErrInvalid
	}
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return err
	}
	if epoch != batch.Revocation {
		return ErrJobConflict
	}
	blocked, err := restrictedEvents(ctx, tx, RecallScope{Agent: batch.Agent, Person: batch.Person, ExternalModel: true})
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, u := range batch.Utterances {
		for _, e := range u.Events {
			if e.Kind != Event || e.Precision != Exact || !present(e.ID) {
				return ErrEvidence
			}
			allowed[e.ID] = true
		}
	}
	for _, item := range items {
		for _, e := range item.Evidence {
			if e.Kind != Event || e.Precision != Exact || !allowed[e.ID] {
				return ErrEvidence
			}
			if blocked[e.ID] {
				return ErrScope
			}
		}
		if _, err := Insert(ctx, tx, Write{Agent: batch.Agent, Person: batch.Person, Item: item}, resolve); err != nil {
			return err
		}
	}
	return nil
}

// CheckExtraction is called by Core's model callback immediately before Aux
// dispatch. It checks both the revocation epoch and named scopes, since a scope
// can tighten after a task was enqueued without changing that epoch.
// On ErrJobConflict the Core model callback returns [] with zero usage without
// dispatching. The scheduler rebuilds a replacement from batch.After, so still
// eligible events are not lost when Apply consumes the obsolete task.
func (s *Store) CheckExtraction(ctx context.Context, batch ExtractionBatch, resolve ResolveEvidence) error {
	if s == nil || s.db == nil || resolve == nil || !present(batch.Agent) || batch.Agent == string(All) || !present(batch.Person) {
		return ErrInvalid
	}
	return s.db.Read(ctx, func(tx *canon.Tx) error { return checkPreparedExtraction(ctx, tx, batch, resolve) })
}

func checkPreparedExtraction(ctx context.Context, tx *canon.Tx, batch ExtractionBatch, resolve ResolveEvidence) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return err
	}
	if epoch != batch.Revocation {
		return ErrJobConflict
	}
	blocked, err := restrictedEvents(ctx, tx, RecallScope{Agent: batch.Agent, Person: batch.Person, ExternalModel: true})
	if err != nil {
		return err
	}
	for _, utterance := range batch.Utterances {
		var texts []string
		for _, ref := range utterance.Events {
			if ref.Kind != Event || ref.Precision != Exact {
				return ErrEvidence
			}
			if blocked[ref.ID] {
				return ErrJobConflict
			}
			var visible int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_visible_event WHERE id=?", ref.ID).Scan(&visible); err != nil {
				return err
			}
			if visible != 1 {
				return ErrJobConflict
			}
			origin, err := resolve(ctx, tx, ref)
			if err != nil {
				return err
			}
			if origin.PlanEnded {
				return ErrJobConflict
			}
			if origin.Speaker != guard.Person || origin.Person != batch.Person || origin.Agent != batch.Agent {
				return ErrPersonEvidence
			}
			if !validScope(origin.Scope) {
				return ErrScope
			}
			if origin.Scope == NeverLeaves {
				return ErrJobConflict
			}
			text, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			if text != ref.Quote {
				return ErrJobConflict
			}
			texts = append(texts, text)
		}
		if strings.Join(texts, "\n") != utterance.Text {
			return ErrJobConflict
		}
	}
	return nil
}

// ExtractionApply binds the job to its agent and person at registration, and
// has the signature of jobs.Definition.Apply without importing a higher layer.
func ExtractionApply(agent, person string, resolve ResolveEvidence) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		var batch ExtractionBatch
		var items []Item
		if err := json.Unmarshal(task.Input, &batch); err != nil {
			return ErrInvalid
		}
		if batch.Agent != agent || batch.Person != person {
			return ErrScope
		}
		if err := json.Unmarshal(output, &items); err != nil {
			return ErrInvalid
		}
		if tx == nil || resolve == nil {
			return ErrInvalid
		}
		// Determine obsolescence before the first item write. Never swallow an
		// error from ApplyExtraction after it has begun inserting a batch.
		if err := checkPreparedExtraction(ctx, tx, batch, resolve); err != nil {
			if errors.Is(err, ErrJobConflict) {
				return nil
			}
			return err
		}
		return ApplyExtraction(ctx, tx, batch, items, resolve)
	}
}

func instructionLike(text string) bool {
	text = normalize(text)
	for _, prefix := range []string{"ignore previous", "ignore all", "you must ", "always obey", "system:", "developer:", "answer ", "respond ", "이전 지시", "항상 답", "무조건 "} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
