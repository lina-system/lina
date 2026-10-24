// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/assemble/atropos"
	"github.com/thisisjun786/lina/internal/core/binding"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func bindingRevision(state binding.State) string {
	raw, _ := json.Marshal(state)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (r *conversationRuntime) judgeInput(ctx context.Context, input ledger.Event) error {
	judge := r.inputJudge
	if r.router != nil {
		judge = r.router.root.inputJudge
	}
	if judge == nil || input.SpeakerKind != guard.Person || r.channelBinding != nil && !r.channelBinding.Scope.Tools {
		return nil
	}
	initial, err := r.journal().Get(ctx, inputBindingID(input.ID))
	if errors.Is(err, sql.ErrNoRows) {
		err = r.db.Write(ctx, func(tx *canon.Tx) error { return r.bindInput(ctx, tx, input) })
		if err == nil {
			initial, err = r.journal().Get(ctx, inputBindingID(input.ID))
		}
	}
	if err != nil {
		return err
	}
	var d binding.Decision
	if err = json.Unmarshal([]byte(initial.Body.Text()), &d); err != nil {
		return err
	}
	if !d.Unresolved {
		return nil
	}
	var state binding.State
	var count int
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM conv_event WHERE parent_id=? AND scaffold_kind IN (?,?)`, input.ID, modelBindingKind, "input_binding_judgment").Scan(&count); err != nil {
			return err
		}
		var e error
		state, e = r.bindingState(ctx, tx, input)
		return e
	})
	if err != nil || count > 0 {
		return err
	}
	q := atropos.ClosedChoice{ID: input.ID, Revision: bindingRevision(state), Input: input.Body.Text()}
	targets := map[string]binding.Target{"none": {Class: "none"}}
	for _, i := range d.Candidates {
		if binding.Open(i) {
			key := "item:" + i.ID
			q.Candidates = append(q.Candidates, atropos.Candidate{ID: key, Description: i.Title + " / " + i.Kind + " / " + i.State})
			targets[key] = binding.Target{Class: "existing", ID: i.ID, Kind: i.Kind}
		}
	}
	for _, key := range []string{"new", "none", "not-applicable", "hold"} {
		q.Candidates = append(q.Candidates, atropos.Candidate{ID: key, Description: key})
	}
	report := atropos.JudgeChoices(ctx, judge, []atropos.ClosedChoice{q}, atropos.DefaultCap, 0.8)
	// Record every attempted call, including a handoff, with observed latency.
	raw, _ := json.Marshal(struct {
		Revision  string                      `json:"revision"`
		LatencyNS int64                       `json:"latency_ns"`
		Answers   map[string]atropos.Judgment `json:"answers"`
	}{q.Revision, int64(report.Latency), report.Answers})
	_, err = r.journal().Admit(ctx, guard.EntrySystem, ledger.Input{ID: "binding-judgment:" + input.ID, ParentID: input.ID, ConversationID: input.ConversationID, AgentID: input.AgentID, Surface: input.Surface, ScaffoldKind: "input_binding_judgment", OccurredAt: input.RecordedAt, RecordedAt: time.Now().UTC(), TimeQuality: "observed", Body: string(raw), Relations: []ledger.Relation{{TargetID: input.ID, Kind: "view_of"}}})
	if err != nil {
		return err
	}
	answer, ok := report.Answers[q.ID]
	if !ok {
		return nil
	}
	// A generic new choice still needs the main model's kind, parent and duplicate
	// check; a judge cannot infer authority or split requests by confidence alone.
	target, ok := targets[answer.Choice]
	if !ok {
		return nil
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		fresh, err := r.bindingState(ctx, tx, input)
		if err != nil {
			return err
		}
		if bindingRevision(fresh) != q.Revision {
			return nil
		}
		choice := binding.Choice{Request: 0, Quote: input.Body.Text(), Target: target, Decider: "judge", Probabilities: answer.Probabilities}
		return appendInputChoices(ctx, tx, input, "binding-judge:"+input.ID, []binding.Choice{choice})
	})
}
