// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"reflect"
)

// EvidenceReader is the installed adapter's read-only revalidation boundary.
// It reads the external target or retained check output again. It must never
// replay the effect whose result is being checked.
type EvidenceReader interface {
	RereadEvidence(context.Context, Action, CriterionEvidence) (CriterionEvidence, error)
}

type evidenceRead struct {
	token            string
	running, current bool
	cancel           context.CancelFunc
}

// Read-only revalidation has no effect to redispatch after a crash. Keep at most
// one reader per goal; the journal and close token fence its eventual result.
// Run owns its context and joins it at shutdown. Startup derivation only queues
// work, so a slow connector cannot delay publishing Core's ingress.
func (e *Engine) scheduleEvidence(reader EvidenceReader, g Goal, refresh bool) bool {
	token := evidenceToken(g)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.evidenceCtx == nil || e.evidenceCtx.Err() != nil {
		return false
	}
	if old := e.evidenceReads[g.ID]; old != nil {
		if old.running {
			if old.token != token {
				old.cancel()
			}
			return false
		}
		if !refresh && old.token == token {
			return old.current
		}
	}
	ctx, cancel := context.WithCancel(e.evidenceCtx)
	flight := &evidenceRead{token: token, running: true, cancel: cancel}
	e.evidenceReads[g.ID] = flight
	e.wg.Go(func() {
		current, err := e.rereadEvidence(ctx, reader, g)
		e.mu.Lock()
		flight.running, flight.current = false, current && err == nil
		e.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			select {
			case e.failures <- err:
			default:
			}
		}
		cancel()
		e.journal.notify()
	})
	return false
}

func evidenceToken(g Goal) string {
	return string(encode(struct {
		Revision int64
		Round    int64
		Checks   []CriterionEvidence
	}{g.Revision, g.CheckRound, g.Checks}))
}

// RefreshEvidence runs on startup and the safety sweep, including for completed
// goals. The current revision/round fences a delayed read against newer work.
func (e *Engine) RefreshEvidence(ctx context.Context) error {
	reader, ok := e.cfg.Driver.(EvidenceReader)
	if !ok {
		return nil
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, g := range s.Goals {
		if g.State == GoalStopped || g.DefinitionPending || len(g.Checks) == 0 {
			continue
		}
		e.scheduleEvidence(reader, g, true)
	}
	return nil
}

// Closing checks happen outside the canon writer transaction. The transaction
// compares this token again before closing, so a concurrent change wins.
func (e *Engine) closingEvidence(ctx context.Context) (map[string]string, error) {
	reader, ok := e.cfg.Driver.(EvidenceReader)
	if !ok {
		return nil, nil
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	p, err := Derive(s, e.cfg.Now())
	if err != nil {
		return nil, err
	}
	verified := map[string]string{}
	for _, g := range s.Goals {
		if !has(p.Close, g.ID) {
			continue
		}
		if e.scheduleEvidence(reader, g, false) {
			verified[g.ID] = evidenceToken(g)
		}
	}
	return verified, nil
}

func (e *Engine) rereadEvidence(ctx context.Context, reader EvidenceReader, g Goal) (bool, error) {
	changed := false
	var reads []CriterionEvidence
	for _, old := range g.Checks {
		if !old.Passed {
			continue
		}
		var c Criterion
		for _, candidate := range goalCriteria(g) {
			if candidate.ID == old.Criterion {
				c = candidate
				break
			}
		}
		if !currentEvidence(g, c, old) {
			changed = true
			continue
		}
		a := Action{ID: "evidence-read:" + rand.Text(), GoalID: g.ID, Kind: CheckCriterion, CriterionID: c.ID, Check: &c.Check, Revision: old.Revision, GoalRevision: g.Revision, CheckRound: g.CheckRound, Epoch: e.cfg.Epoch}
		a.InputVersion = inputVersion(g, a)
		fresh, err := reader.RereadEvidence(ctx, a, old)
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		reads = append(reads, fresh)
		if err != nil || !currentEvidence(g, c, fresh) || !fresh.Passed || !fresh.Collected.Matches(c.Check) || !reflect.DeepEqual(old.Collected, fresh.Collected) {
			changed = true
		}
	}
	kind := "evidence.rechecked"
	if changed {
		kind = "evidence.invalidated"
	}
	err := e.journal.Update(ctx, Transition{ID: "evidence-stale:" + rand.Text(), Kind: kind, At: e.cfg.Now().Unix(), Data: encode(map[string]any{"goal_id": g.ID, "epoch": e.cfg.Epoch, "reads": reads})}, func(v *View) error {
		current, err := loadGoal(v, g.ID)
		if err != nil {
			return err
		}
		if err = e.hydrate(&current); err != nil {
			return err
		}
		if current.State == GoalStopped || evidenceToken(current) != evidenceToken(g) {
			return errNoChange
		}
		if !changed {
			return nil
		}
		reopenEvidence(&current)
		if err = retireEvidenceWaits(v, current); err != nil {
			return err
		}
		return put(v, "goal", current.ID, current)
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	return !changed && err == nil, err
}

func reopenEvidence(g *Goal) {
	g.Checks = nil
	g.CheckRound++
	g.AssessedFailure, g.AttentionID, g.BlockedReason = "", "", ""
	g.State = GoalRunning
}

// TargetChanged accepts a trusted integration's change hint, not proof of a
// successful effect. A duplicate event is idempotent. All evidence is recollected
// in a new checking round; historical observations remain in the journal.
func (e *Engine) TargetChanged(ctx context.Context, id, target string) error {
	if id == "" || target == "" {
		return ErrInvalid
	}
	return e.journal.Update(ctx, Transition{ID: "target-changed:" + id, Kind: "evidence.target_changed", At: 1, Data: encode(target)}, func(v *View) error {
		s, err := snapshotView(v)
		if err != nil {
			return err
		}
		for _, g := range s.Goals {
			if g.State == GoalStopped {
				continue
			}
			if err = e.hydrate(&g); err != nil {
				return err
			}
			matched := false
			for _, c := range goalCriteria(g) {
				matched = matched || c.Check.Target == target
			}
			for _, check := range g.Checks {
				if check.Collected != nil {
					for _, read := range check.Collected.Reads {
						matched = matched || read.Target == target
					}
				}
			}
			if !matched {
				continue
			}
			reopenEvidence(&g)
			if err = retireEvidenceWaits(v, g); err != nil {
				return err
			}
			if err = put(v, "goal", g.ID, g); err != nil {
				return err
			}
		}
		return nil
	})
}

func failedEvidence(g Goal) bool {
	for _, c := range goalCriteria(g) {
		for _, proof := range g.Checks {
			if currentEvidence(g, c, proof) && (!proof.Passed || !proof.Collected.Matches(c.Check)) {
				return true
			}
		}
	}
	return false
}

// Authorization refused before execution. An old round must not be revived by
// a late approval hint with a new InputVersion and its old Action.CheckRound.
func retireEvidenceWaits(v *View, g Goal) error {
	for _, row := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(row.Body, &a); err != nil {
			return err
		}
		if a.GoalID != g.ID || (a.Kind != CheckCriterion && (a.Kind != PlanGoal || a.TaskID != "")) || (a.State != "queued" && a.State != "awaiting_authorization") || !staleInputs(g, a) {
			continue
		}
		a.State, a.Superseded, a.RetirementReconciled = "finished", true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}
