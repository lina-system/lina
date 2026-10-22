// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type Affect struct {
	Valence float64 `json:"valence"`
	Arousal float64 `json:"arousal"`
}
type ShortEmotion struct {
	Affect    Affect    `json:"affect"`
	EventID   string    `json:"event_id"`
	At        time.Time `json:"at"`
	Sensitive bool      `json:"sensitive,omitempty"`
}
type EmotionRecord struct {
	Disposition Affect                  `json:"disposition"`
	Mood        Affect                  `json:"mood"`
	MoodAt      time.Time               `json:"mood_at"`
	Short       map[string]ShortEmotion `json:"short"`
	Growth      map[int64]GrowthRecord  `json:"growth,omitempty"`
	Undone      map[string]bool         `json:"undone,omitempty"`
}
type EmotionPolicy struct {
	MoodHalfLife  time.Duration
	ShortLifetime time.Duration
	EventWeight   float64
}

func DefaultEmotionPolicy() EmotionPolicy {
	return EmotionPolicy{MoodHalfLife: 72 * time.Hour, ShortLifetime: 30 * time.Minute, EventWeight: 0.25}
}
func (p EmotionPolicy) valid() bool {
	return p.MoodHalfLife > 0 && p.ShortLifetime > 0 && finite(p.EventWeight) && p.EventWeight > 0 && p.EventWeight <= 1
}
func finite(n float64) bool { return !math.IsNaN(n) && !math.IsInf(n, 0) }
func (a Affect) valid() bool {
	return finite(a.Valence) && finite(a.Arousal) && math.Abs(a.Valence) <= 1 && math.Abs(a.Arousal) <= 1
}
func blend(a, b Affect, weight float64) Affect {
	return Affect{Valence: a.Valence + (b.Valence-a.Valence)*weight, Arousal: a.Arousal + (b.Arousal-a.Arousal)*weight}
}
func moodAt(record *EmotionRecord, at time.Time, policy EmotionPolicy, baseline Affect) Affect {
	if record.MoodAt.IsZero() {
		return baseline
	}
	elapsed := max(at.Sub(record.MoodAt), 0)
	return blend(baseline, record.Mood, math.Exp2(-float64(elapsed)/float64(policy.MoodHalfLife)))
}

type EmotionSnapshot struct {
	Short       Affect `json:"short"`
	Mood        Affect `json:"mood"`
	Disposition Affect `json:"disposition"`
	Suppressed  bool   `json:"suppressed"`
}

// ProjectEmotion is clock-driven and pure. A pinned persona revision keeps its
// disposition even if another turn has adopted a newer growth revision.
// Suppression hides all expressive values without changing the durable record.
func ProjectEmotion(state State, conversation string, revision int64, at time.Time, policy EmotionPolicy, sensitive bool) (EmotionSnapshot, error) {
	if conversation == "" || revision <= 0 || at.IsZero() || !policy.valid() {
		return EmotionSnapshot{}, ErrInvalid
	}
	out := EmotionSnapshot{Suppressed: state.ExpressionOff || sensitive}
	if state.Affect == nil {
		return out, nil
	}
	r := state.Affect
	if !r.Disposition.valid() || !r.Mood.valid() {
		return EmotionSnapshot{}, ErrCorrupt
	}
	baseline := r.Disposition
	if len(r.Growth) > 0 {
		baseline = Affect{}
		latest := int64(0)
		for rev, growth := range r.Growth {
			if rev <= revision && rev > latest {
				baseline = growth.After
				latest = rev
			}
		}
	}
	out.Disposition = baseline
	out.Mood = moodAt(r, at, policy, baseline)
	if short, ok := r.Short[conversation]; ok {
		if !at.Before(short.At) && at.Sub(short.At) < policy.ShortLifetime {
			out.Short = short.Affect
			out.Suppressed = out.Suppressed || short.Sensitive
		}
	}
	if out.Suppressed {
		out.Short = Affect{}
		out.Mood = Affect{}
		out.Disposition = Affect{}
	}
	return out, nil
}

type EmotionResult struct {
	Recorded bool   `json:"recorded"`
	Revision string `json:"revision"`
}
type ExpressionResult struct {
	Changed  bool   `json:"changed"`
	Enabled  bool   `json:"enabled"`
	Revision string `json:"revision"`
}

type EmotionEvent struct {
	ConversationID, EventID string
	Affect                  Affect
	At                      time.Time
	Sensitive               bool
}

func readState(ctx context.Context, tx *canon.Tx, id string) (State, error) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT state FROM agents WHERE id=? AND trashed_at=0", id).Scan(&raw); err != nil {
		return State{}, found(err)
	}
	var state State
	err := json.Unmarshal(raw, &state)
	return state, err
}
func writeState(ctx context.Context, tx *canon.Tx, id string, state State) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE agents SET state=? WHERE id=?", raw, id)
	return err
}

// RecordEmotion is the store part of Core's confirmed-event Moirai writer.
// The durable action journal makes replay of one canonical event a no-op.
func (s *Store) RecordEmotion(ctx context.Context, id, expected string, event EmotionEvent, person string, policy EmotionPolicy) (EmotionResult, error) {
	if !present(person) || !present(event.ConversationID) || !present(event.EventID) || event.At.IsZero() || !event.Affect.valid() || !policy.valid() || expected == "" {
		return EmotionResult{}, ErrInvalid
	}
	result := EmotionResult{}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		state, err := readState(ctx, tx, id)
		if err != nil {
			return err
		}
		if StateRevision(state) != expected {
			return ErrConflict
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM agent_actions WHERE agent_id=? AND action=?", id, "emotion:"+event.EventID).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			result.Revision = expected
			return nil
		}
		if state.Affect == nil {
			state.Affect = &EmotionRecord{}
		}
		r := state.Affect
		if !r.MoodAt.IsZero() && event.At.Before(r.MoodAt) {
			return ErrInvalid
		}
		r.Mood = blend(moodAt(r, event.At, policy, r.Disposition), event.Affect, policy.EventWeight)
		r.MoodAt = event.At.UTC()
		if r.Short == nil {
			r.Short = map[string]ShortEmotion{}
		}
		r.Short[event.ConversationID] = ShortEmotion{Affect: event.Affect, EventID: event.EventID, At: event.At.UTC(), Sensitive: event.Sensitive}
		if err := writeState(ctx, tx, id, state); err != nil {
			return err
		}
		if err := action(ctx, tx, id, person, "emotion:"+event.EventID, event.At.UnixNano()); err != nil {
			return err
		}
		result = EmotionResult{Recorded: true, Revision: StateRevision(state)}
		return nil
	})
	if err != nil {
		return EmotionResult{}, err
	}
	return result, nil
}

// ExpressionRevision is independent of emotion and blank-setting updates, so
// turning expression off cannot be blocked by this turn's own emotion write.
func ExpressionRevision(state State) string {
	raw, _ := json.Marshal(struct {
		Off    bool
		Source string
	}{state.ExpressionOff, state.ExpressionSource})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Store) SetEmotionExpression(ctx context.Context, id, expected string, enabled bool, event, person string) (ExpressionResult, error) {
	if expected == "" || !present(event) || !present(person) {
		return ExpressionResult{}, ErrInvalid
	}
	result := ExpressionResult{}
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := get(ctx, tx, id, false); err != nil {
			return err
		}
		state, err := readState(ctx, tx, id)
		if err != nil {
			return err
		}
		if ExpressionRevision(state) != expected {
			return ErrConflict
		}
		result = ExpressionResult{Enabled: enabled, Revision: expected}
		if state.ExpressionOff == !enabled && state.ExpressionSource != "" {
			return nil
		}
		state.ExpressionOff = !enabled
		state.ExpressionSource = event
		if err := writeState(ctx, tx, id, state); err != nil {
			return err
		}
		if err := action(ctx, tx, id, person, "expression:"+event, 0); err != nil {
			return err
		}
		result.Changed = true
		result.Revision = ExpressionRevision(state)
		return nil
	})
	if err != nil {
		return ExpressionResult{}, err
	}
	return result, nil
}
