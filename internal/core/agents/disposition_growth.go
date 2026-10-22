// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/secrets"
)

var ErrGrowthEvaluation = errors.New("growth_requires_improved_evaluation")

type GrowthEvaluationRecord struct {
	Metric         string   `json:"metric"`
	SetID          string   `json:"set_id"`
	Cases          []string `json:"cases"`
	BaselineScore  float64  `json:"baseline_score"`
	CandidateScore float64  `json:"candidate_score"`
}
type GrowthRecord struct {
	Before     Affect                 `json:"before"`
	After      Affect                 `json:"after"`
	Hash       string                 `json:"hash"`
	Evaluation GrowthEvaluationRecord `json:"evaluation"`
	Undone     bool                   `json:"undone,omitempty"`
}

type DispositionCandidate struct {
	Snapshot    string `json:"snapshot"`
	Disposition Affect `json:"disposition"`
}
type GrowthCase struct{ ID, Input string }

// Score is a trusted independent, pure metric, with higher scores better. It
// is never supplied by a model or registered as a conversation tool. No growth
// mechanism is enabled by default; missing or non-improving evaluations stay off.
type GrowthEvaluation struct {
	Metric, SetID string
	Cases         []GrowthCase
	Score         func(DispositionCandidate, GrowthCase) (float64, error)
}

// GrowthProof cannot be constructed from provider JSON. Measurement binds both
// candidates, the exact case set and the agent's expected persona revision.
type GrowthProof struct {
	agent               string
	revision            int64
	baseline, candidate DispositionCandidate
	hash                string
	evaluation          GrowthEvaluationRecord
	improved            bool
}

func candidateHash(candidate DispositionCandidate) string {
	raw, _ := json.Marshal(candidate)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (s *Store) MeasureDispositionGrowth(ctx context.Context, id string, expected int64, candidate DispositionCandidate, evaluation GrowthEvaluation) (GrowthProof, error) {
	masked, err := secrets.Mask(candidate.Snapshot)
	if err != nil || masked.Text() != candidate.Snapshot || !utf8.ValidString(candidate.Snapshot) || len(candidate.Snapshot) > 32768 || !candidate.Disposition.valid() || !present(evaluation.Metric) || !present(evaluation.SetID) || len(evaluation.Cases) == 0 || len(evaluation.Cases) > 1024 || evaluation.Score == nil {
		return GrowthProof{}, ErrInvalid
	}
	a, err := s.Get(ctx, id)
	if err != nil {
		return GrowthProof{}, err
	}
	if a.Revision != expected {
		return GrowthProof{}, ErrConflict
	}
	previous, err := s.revision(ctx, id, expected)
	if err != nil {
		return GrowthProof{}, err
	}
	state, err := s.State(ctx, id)
	if err != nil {
		return GrowthProof{}, err
	}
	baseline := DispositionCandidate{Snapshot: previous.Layers.GrowthSnapshot}
	hash := candidateHash(candidate)
	if state.Affect != nil {
		baseline.Disposition = state.Affect.Disposition
		if state.Affect.Undone[hash] {
			return GrowthProof{}, ErrGrowthUndone
		}
	}
	proof := GrowthProof{agent: id, revision: expected, baseline: baseline, candidate: candidate, hash: hash, evaluation: GrowthEvaluationRecord{Metric: evaluation.Metric, SetID: evaluation.SetID}}
	seen := map[string]bool{}
	for _, example := range evaluation.Cases {
		if err := ctx.Err(); err != nil {
			return GrowthProof{}, err
		}
		if !present(example.ID) || seen[example.ID] {
			return GrowthProof{}, ErrInvalid
		}
		seen[example.ID] = true
		base, err := evaluation.Score(baseline, example)
		if err != nil {
			return GrowthProof{}, err
		}
		changed, err := evaluation.Score(candidate, example)
		if err != nil {
			return GrowthProof{}, err
		}
		if !finite(base) || !finite(changed) {
			return GrowthProof{}, ErrInvalid
		}
		proof.evaluation.Cases = append(proof.evaluation.Cases, example.ID)
		proof.evaluation.BaselineScore += base / float64(len(evaluation.Cases))
		proof.evaluation.CandidateScore += changed / float64(len(evaluation.Cases))
	}
	proof.improved = finite(proof.evaluation.CandidateScore) && finite(proof.evaluation.BaselineScore) && proof.evaluation.CandidateScore > proof.evaluation.BaselineScore
	return proof, nil
}

func (s *Store) AdoptDispositionGrowth(ctx context.Context, id string, expected int64, proof GrowthProof, person string, at time.Time, policy EmotionPolicy) (Agent, error) {
	if proof.agent != id || proof.revision != expected || !proof.improved || proof.hash == "" {
		return Agent{}, ErrGrowthEvaluation
	}
	if at.IsZero() || !policy.valid() {
		return Agent{}, ErrInvalid
	}
	previous, err := s.revision(ctx, id, expected)
	if err != nil {
		return Agent{}, err
	}
	if previous.Layers.GrowthSnapshot != proof.baseline.Snapshot {
		return Agent{}, ErrConflict
	}
	layers := previous.Layers
	layers.GrowthSnapshot = proof.candidate.Snapshot
	return s.revise(ctx, id, expected, layers, person, func(tx *canon.Tx) error {
		state, err := readState(ctx, tx, id)
		if err != nil {
			return err
		}
		if state.Affect == nil {
			state.Affect = &EmotionRecord{}
		}
		r := state.Affect
		if r.Disposition != proof.baseline.Disposition {
			return ErrConflict
		}
		if r.Undone[proof.hash] {
			return ErrGrowthUndone
		}
		if !r.MoodAt.IsZero() && at.Before(r.MoodAt) {
			return ErrInvalid
		}
		r.Mood = moodAt(r, at, policy, r.Disposition)
		r.MoodAt = at.UTC()
		if r.Growth == nil {
			r.Growth = map[int64]GrowthRecord{}
		}
		r.Growth[expected+1] = GrowthRecord{Before: r.Disposition, After: proof.candidate.Disposition, Hash: proof.hash, Evaluation: proof.evaluation}
		r.Disposition = proof.candidate.Disposition
		if err := writeState(ctx, tx, id, state); err != nil {
			return err
		}
		return action(ctx, tx, id, person, "growth:"+strconv.FormatInt(expected+1, 10), at.UnixNano())
	})
}

func (s *Store) UndoDispositionGrowth(ctx context.Context, id string, expected int64, person string, at time.Time, policy EmotionPolicy) (Agent, error) {
	if expected <= 1 || at.IsZero() || !policy.valid() {
		return Agent{}, ErrInvalid
	}
	current, err := s.revision(ctx, id, expected)
	if err != nil {
		return Agent{}, err
	}
	previous, err := s.revision(ctx, id, expected-1)
	if err != nil {
		return Agent{}, err
	}
	layers := current.Layers
	layers.GrowthSnapshot = previous.Layers.GrowthSnapshot
	if layers != previous.Layers {
		return Agent{}, ErrInvalid
	}
	return s.revise(ctx, id, expected, layers, person, func(tx *canon.Tx) error {
		state, err := readState(ctx, tx, id)
		if err != nil {
			return err
		}
		if state.Affect == nil {
			return ErrInvalid
		}
		r := state.Affect
		step, ok := r.Growth[expected]
		if !ok || step.Hash == "" || step.Undone {
			return ErrInvalid
		}
		if !r.MoodAt.IsZero() && at.Before(r.MoodAt) {
			return ErrInvalid
		}
		r.Mood = moodAt(r, at, policy, r.Disposition)
		r.MoodAt = at.UTC()
		step.Undone = true
		r.Growth[expected] = step
		r.Growth[expected+1] = GrowthRecord{Before: r.Disposition, After: step.Before}
		r.Disposition = step.Before
		if r.Undone == nil {
			r.Undone = map[string]bool{}
		}
		r.Undone[step.Hash] = true
		if err := writeState(ctx, tx, id, state); err != nil {
			return err
		}
		return action(ctx, tx, id, person, "growth_undo:"+strconv.FormatInt(expected, 10), at.UnixNano())
	})
}
