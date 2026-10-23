// SPDX-License-Identifier: Apache-2.0
package memory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

type BriefSource struct {
	Evidence Evidence `json:"evidence"`
	Text     string   `json:"text"`
}
type BriefSentence struct {
	Text      string     `json:"text"`
	Citations []Evidence `json:"citations"`
}
type BriefTask struct {
	Scope      RecallScope   `json:"scope"`
	Topic      string        `json:"topic"`
	Generation int64         `json:"generation"`
	Turn       int64         `json:"turn"`
	Cadence    int64         `json:"cadence"`
	Revocation int64         `json:"revocation"`
	Sources    []BriefSource `json:"sources"`
}
type briefRecord struct {
	Task        BriefTask       `json:"task"`
	Sentences   []BriefSentence `json:"sentences"`
	EmptyStreak int64           `json:"empty_streak"`
	NextTurn    int64           `json:"next_turn"`
	Completed   bool            `json:"completed"`
}

// BriefCadence follows Honcho's bounded additive backoff, not an exponential
// delay. Explicit turn counters make the cadence and expiry reproducible.
func BriefCadence(base, emptyStreak int64) int64 {
	if base < 1 || base > 1000000 {
		return 0
	}
	if emptyStreak < 0 {
		emptyStreak = 0
	}
	return base + min(emptyStreak, base*7)
}

// PrepareBrief is called by Core after delivery, before enqueueing a job. It
// reserves a generation; only that generation can publish. Sources are bounded
// and filtered for model scope before dispatch. This is not the request path.
func (s *Store) PrepareBrief(ctx context.Context, scope RecallScope, topic string, turn, cadence, at int64, recent []Evidence, resolve ResolveEvidence) (BriefTask, bool, error) {
	task := BriefTask{Scope: scope, Topic: topic, Turn: turn, Cadence: cadence}
	if s == nil || s.db == nil || !present(topic) || turn < 0 || at < 0 || BriefCadence(cadence, 0) == 0 || len(recent) > 12 || resolve == nil {
		return task, false, ErrInvalid
	}
	scope.ExternalModel = true
	task.Scope = scope
	hits, err := s.Recall(ctx, scope, Query{Text: topic, At: at, Limit: 12, PersonOnly: true})
	if err != nil {
		return task, false, err
	}
	profile, err := s.Profile(ctx, scope, at, 12, 4096, func(s string) int { return len([]rune(s)) })
	if err != nil {
		return task, false, err
	}
	seen := map[string]bool{}
	for _, hit := range append(profile, hits...) {
		if seen[hit.ID] || len(task.Sources) >= 12 {
			continue
		}
		seen[hit.ID] = true
		task.Sources = append(task.Sources, BriefSource{Evidence: Evidence{Kind: MemoryItem, ID: hit.ID, Revision: hit.Revision}, Text: hit.Content})
	}
	ready := false
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		current, err := readBrief(ctx, tx, scope.Person, scope.Agent)
		if err != nil {
			return err
		}
		if current.Task.Topic == topic && turn < current.NextTurn {
			return nil
		}
		if current.Task.Generation > 0 && turn < current.Task.Turn {
			return ErrJobConflict
		}
		task.Generation = current.Task.Generation + 1
		if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&task.Revocation); err != nil {
			return err
		}
		for _, ref := range recent {
			if len(task.Sources) >= 12 {
				break
			}
			if ref.Kind != Event || ref.Precision != Exact {
				return ErrEvidence
			}
			origin, err := resolve(ctx, tx, ref)
			if err != nil {
				return err
			}
			if origin.ID != "" {
				ref.ID = origin.ID
			}
			if slices.ContainsFunc(task.Sources, func(s BriefSource) bool { return s.Evidence.Kind == Event && s.Evidence.ID == ref.ID }) {
				continue
			}
			if origin.Person != scope.Person || origin.Agent != scope.Agent || origin.Speaker != guard.Person || origin.Scope == NeverLeaves || origin.PlanEnded {
				return ErrEvidence
			}
			text, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			task.Sources = append(task.Sources, BriefSource{Evidence: Evidence{Kind: Event, ID: ref.ID, Precision: Exact}, Text: text})
		}
		if len(task.Sources) == 0 {
			return nil
		}
		if err := validateBriefSources(ctx, tx, task, scope, at, resolve); err != nil {
			return err
		}
		streak := current.EmptyStreak
		if current.Task.Topic != topic {
			streak = 0
		}
		if err := writeBrief(ctx, tx, briefRecord{Task: task, EmptyStreak: streak}); err != nil {
			return err
		}
		ready = true
		return nil
	})
	return task, ready, err
}

// PublishBrief validates citations against the task and current canon. Unknown
// citations are dropped, as are sentences without a surviving citation. Empty
// output increases the cadence; a stale job cannot change either output or backoff.
func PublishBrief(ctx context.Context, tx *canon.Tx, task BriefTask, sentences []BriefSentence, at int64, resolve ResolveEvidence) error {
	if tx == nil || resolve == nil || at < 0 || len(sentences) > 32 {
		return ErrInvalid
	}
	current, err := readBrief(ctx, tx, task.Scope.Person, task.Scope.Agent)
	if err != nil {
		return err
	}
	expected, _ := json.Marshal(current.Task)
	actual, _ := json.Marshal(task)
	if task.Generation == 0 || current.Completed || !bytes.Equal(expected, actual) {
		return ErrJobConflict
	}
	if err := validateBriefSources(ctx, tx, task, task.Scope, at, resolve); err != nil {
		return err
	}
	for _, sentence := range sentences {
		if !present(sentence.Text) {
			continue
		}
		masked, err := maskedJobText(sentence.Text)
		if err != nil {
			return err
		}
		if masked != sentence.Text {
			return ErrInvalid
		}
		var refs []Evidence
		for _, citation := range sentence.Citations {
			for _, source := range task.Sources {
				if citation.Kind == source.Evidence.Kind && citation.ID == source.Evidence.ID && citation.Revision == source.Evidence.Revision && !slices.Contains(refs, source.Evidence) {
					refs = append(refs, source.Evidence)
					break
				}
			}
		}
		if len(refs) > 0 {
			current.Sentences = append(current.Sentences, BriefSentence{Text: sentence.Text, Citations: refs})
		}
	}
	current.Completed = true
	if len(current.Sentences) > 0 {
		current.EmptyStreak = 0
	} else {
		current.EmptyStreak = min(current.EmptyStreak+1, task.Cadence*7)
	}
	current.NextTurn = task.Turn + BriefCadence(task.Cadence, current.EmptyStreak)
	return writeBrief(ctx, tx, current)
}

// CheckBrief is the Core model callback's pre-dispatch guard. A queued task may
// have become obsolete before the worker requests Aux; no bytes from that task
// may be sent to a model until this check succeeds. ErrJobConflict means the
// callback must return an empty JSON array with zero usage, without a model
// call, so Apply can consume the obsolete task. Apply checks again later.
func (s *Store) CheckBrief(ctx context.Context, task BriefTask, at int64, resolve ResolveEvidence) error {
	if s == nil || s.db == nil || resolve == nil || at < 0 {
		return ErrInvalid
	}
	return s.db.Read(ctx, func(tx *canon.Tx) error {
		current, err := readBrief(ctx, tx, task.Scope.Person, task.Scope.Agent)
		if err != nil {
			return err
		}
		expected, _ := json.Marshal(current.Task)
		actual, _ := json.Marshal(task)
		if task.Generation == 0 || current.Completed || !bytes.Equal(expected, actual) {
			return ErrJobConflict
		}
		err = validateBriefSources(ctx, tx, task, task.Scope, at, resolve)
		if obsoleteBrief(err) {
			return ErrJobConflict
		}
		return err
	})
}

// PersonBrief is a ready-only read. It never waits for a job or calls a model.
// Stale evidence makes the entire brief unavailable, even if only one sentence
// mentions it: its influence may have reached the other generated sentences.
func (s *Store) PersonBrief(ctx context.Context, scope RecallScope, topic string, turn, at int64, resolve ResolveEvidence) ([]BriefSentence, error) {
	if s == nil || s.db == nil || !present(scope.Agent) || !present(scope.Person) || turn < 0 || at < 0 || resolve == nil {
		return nil, ErrInvalid
	}
	var result []BriefSentence
	var generation int64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		current, err := readBrief(ctx, tx, scope.Person, scope.Agent)
		if err != nil {
			return err
		}
		task := current.Task
		if !current.Completed || task.Topic != topic || turn < task.Turn || turn-task.Turn > task.Cadence*2 {
			return nil
		}
		if err := validateBriefSources(ctx, tx, task, scope, at, resolve); err != nil {
			if obsoleteBrief(err) {
				return nil
			}
			return err
		}
		result = current.Sentences
		generation = task.Generation
		return nil
	})
	if err != nil || len(result) == 0 {
		return result, err
	}
	var refs []Evidence
	for _, sentence := range result {
		refs = append(refs, sentence.Citations...)
	}
	refs, err = s.Quotes(ctx, scope, "person_brief", canon.BriefCitationID(scope.Person, scope.Agent), generation, refs)
	if err != nil {
		return nil, err
	}
	offset := 0
	for i := range result {
		n := len(result[i].Citations)
		result[i].Citations = refs[offset : offset+n]
		offset += n
	}
	return result, nil
}
func validateBriefSources(ctx context.Context, tx *canon.Tx, task BriefTask, scope RecallScope, at int64, resolve ResolveEvidence) error {
	var epoch int64
	if err := tx.QueryRowContext(ctx, "SELECT epoch FROM retract_meta WHERE singleton=1").Scan(&epoch); err != nil {
		return err
	}
	if epoch != task.Revocation {
		return ErrJobConflict
	}
	blocked, err := restrictedEvents(ctx, tx, scope)
	if err != nil {
		return err
	}
	for _, source := range task.Sources {
		ref := source.Evidence
		switch ref.Kind {
		case MemoryItem:
			var raw []byte
			err := tx.QueryRowContext(ctx, `SELECT m.record FROM retract_visible_memory m WHERE m.id=? AND m.person=? AND m.holder IN (?,?) AND m.subject_kind='person' AND m.state='active' AND m.valid_from<=? AND (m.valid_until=0 OR m.valid_until>?) AND NOT EXISTS(SELECT 1 FROM memory_item n WHERE n.supersedes=m.id AND n.valid_from<=?)`, ref.ID, scope.Person, scope.Agent, All, at, at, at).Scan(&raw)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrJobConflict
			}
			if err != nil {
				return err
			}
			var item Item
			if err := json.Unmarshal(raw, &item); err != nil {
				return err
			}
			if item.Revision != ref.Revision || item.Content != source.Text {
				return ErrJobConflict
			}
			if scope.Projection && item.Scope != Local || scope.ExternalModel && item.Scope == NeverLeaves {
				return ErrScope
			}
			for _, e := range item.Evidence {
				if e.Kind == Event && blocked[e.ID] {
					return ErrScope
				}
			}
		case Event:
			if blocked[ref.ID] {
				return ErrScope
			}
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM retract_visible_event WHERE id=?", ref.ID).Scan(&exists); err != nil {
				return err
			}
			if exists != 1 {
				return ErrEvidence
			}
			origin, err := resolve(ctx, tx, ref)
			if err != nil {
				return err
			}
			if origin.Speaker != guard.Person || origin.Person != scope.Person || origin.Agent != scope.Agent || origin.PlanEnded {
				return ErrEvidence
			}
			if !validScope(origin.Scope) || scope.Projection && origin.Scope != Local || scope.ExternalModel && origin.Scope == NeverLeaves {
				return ErrScope
			}
			text, err := maskedJobText(origin.Quote)
			if err != nil {
				return err
			}
			if text != source.Text {
				return ErrJobConflict
			}
		case Material, WorkResult, External:
			return ErrEvidence
		default:
			return ErrEvidence
		}
	}
	return nil
}
func readBrief(ctx context.Context, tx *canon.Tx, person, agent string) (briefRecord, error) {
	var result briefRecord
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT record FROM memory_person_brief WHERE person=? AND agent=?", person, agent).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	err = json.Unmarshal(raw, &result)
	return result, err
}
func writeBrief(ctx context.Context, tx *canon.Tx, record briefRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	task := record.Task
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_person_brief VALUES(?,?,?,?) ON CONFLICT(person,agent) DO UPDATE SET generation=excluded.generation,record=excluded.record`, task.Scope.Person, task.Scope.Agent, task.Generation, raw)
	if err != nil {
		return err
	}
	var refs []Evidence
	for _, sentence := range record.Sentences {
		refs = append(refs, sentence.Citations...)
	}
	evidence, err := json.Marshal(refs)
	if err != nil {
		return err
	}
	return canon.WriteEvidenceCitations(ctx, tx, "person_brief", canon.BriefCitationID(task.Scope.Person, task.Scope.Agent), task.Generation, evidence)
}
func BriefApply(agent, person string, now func() int64, resolve ResolveEvidence) func(context.Context, *canon.Tx, jobapi.Task, json.RawMessage) error {
	return func(ctx context.Context, tx *canon.Tx, task jobapi.Task, output json.RawMessage) error {
		if now == nil {
			return ErrInvalid
		}
		var input BriefTask
		var sentences []BriefSentence
		if json.Unmarshal(task.Input, &input) != nil || json.Unmarshal(output, &sentences) != nil {
			return ErrInvalid
		}
		if input.Scope.Person != person || input.Scope.Agent != agent {
			return ErrScope
		}
		if input.Generation < 1 {
			return ErrInvalid
		}
		err := PublishBrief(ctx, tx, input, sentences, now(), resolve)
		// Reject publication but consume an obsolete task, otherwise the job's
		// consecutive watermark would prevent its replacement from ever running.
		if obsoleteBrief(err) {
			return nil
		}
		return err
	}
}

func obsoleteBrief(err error) bool {
	return errors.Is(err, ErrJobConflict) || errors.Is(err, ErrEvidence) || errors.Is(err, ErrScope) || errors.Is(err, sql.ErrNoRows)
}
