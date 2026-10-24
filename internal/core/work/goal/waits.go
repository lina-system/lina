// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"time"
)

// A recorded outside wait keeps its deadline across derivation. Core reads its
// source once for a change signal and once at the deadline, including the
// reconcile that finds a deadline missed while Core was down. Nothing in that
// path sends in the person's name.

func queueClear(g *Goal, w Wait) {
	if w.TodoID == "" {
		return
	}
	for _, c := range g.ClearTodos {
		if c.TodoID == w.TodoID {
			return
		}
	}
	g.ClearTodos = append(g.ClearTodos, ClearedWait{TodoID: w.TodoID, Who: w.Who, Since: w.Since, NextCheck: w.NextCheck})
}

func expiryWait(w Wait) bool {
	return strings.TrimSpace(w.Recommendation) != "" && strings.TrimSpace(w.Draft) != ""
}

func validRecordedWait(w Wait) bool {
	if !namedWait(w) {
		return false
	}
	if (strings.TrimSpace(w.Recommendation) == "") != (strings.TrimSpace(w.Draft) == "") {
		return false
	}
	linked := w.TodoID != "" || w.Who != "" || w.Since != "" || w.NextCheck != ""
	if linked && (strings.TrimSpace(w.TodoID) == "" || strings.TrimSpace(w.Who) == "" || strings.TrimSpace(w.Since) == "" || w.NextCheck != time.Unix(w.Until, 0).UTC().Format(time.DateOnly)) {
		return false
	}
	return true
}

func expiryRow(w Wait) string {
	return strings.TrimSpace(w.Recommendation) + "\n\n" + strings.TrimSpace(w.Draft)
}

func plannedWaits(g Goal, o Observation) ([]Wait, error) {
	waits := append([]Wait(nil), o.Waits...)
	if o.Wait != nil {
		waits = append(waits, *o.Wait)
	}
	seen := map[string]bool{}
	for _, w := range g.Waits {
		if w.Recorded {
			seen[w.Wake] = true
		}
	}
	for _, w := range waits {
		if !validRecordedWait(w) || seen[w.Wake] {
			return nil, ErrInvalid
		}
		seen[w.Wake] = true
	}
	return waits, nil
}

func waitTaskID(wake string) string { return "wait:" + wake }

func readAction(g Goal, w Wait, cause, signal string, now time.Time) Action {
	a := Action{
		GoalID: g.ID, TaskID: waitTaskID(w.Wake), Kind: ReadSource, ReadCause: cause, Signal: signal,
		WaitSource: w.Source, State: "queued", Deadline: now.Add(SafetySweep).Unix(), GoalRevision: g.Revision,
	}
	a.InputVersion = inputVersion(g, a)
	a.ID = actionID(a)
	return a
}

func waitRead(a Action, goal, wake string) bool {
	return a.GoalID == goal && a.Kind == ReadSource && a.TaskID == waitTaskID(wake)
}

func readOpen(actions []Action, goal, wake string) bool {
	for _, a := range actions {
		if waitRead(a, goal, wake) && a.State != "finished" {
			return true
		}
	}
	return false
}

func readSeen(actions []Action, goal, wake, cause, signal string) bool {
	for _, a := range actions {
		if waitRead(a, goal, wake) && a.ReadCause == cause && a.Signal == signal {
			return true
		}
	}
	return false
}

func holdRecordedWaits(plan *Plan, state State, g Goal, now time.Time) bool {
	held := false
	for _, w := range g.Waits {
		if !w.Recorded || !namedWait(w) {
			continue
		}
		w.GoalID = g.ID
		if w.Until <= now.Unix() && !expiryWait(w) {
			continue
		}
		queueDueRead(plan, state, g, &w, now)
		plan.Waits = append(plan.Waits, w)
		held = true
	}
	return held
}

func queueDueRead(plan *Plan, state State, g Goal, w *Wait, now time.Time) {
	if readOpen(state.Actions, g.ID, w.Wake) {
		return
	}
	if w.Until <= now.Unix() && expiryWait(*w) && !w.DeadlineRead && !readSeen(state.Actions, g.ID, w.Wake, "deadline", "") {
		plan.Actions = append(plan.Actions, readAction(g, *w, "deadline", "", now))
		w.DeadlineRead = true
		return
	}
	if w.PendingSignal != "" && !readSeen(state.Actions, g.ID, w.Wake, "signal", w.PendingSignal) {
		plan.Actions = append(plan.Actions, readAction(g, *w, "signal", w.PendingSignal, now))
		w.PendingSignal = ""
	}
}

func actionsOf(v *View) ([]Action, error) {
	out := []Action{}
	for _, r := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(r.Body, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func (e *Engine) applyWaitSignal(v *View, s Signal) error {
	g, err := loadGoal(v, s.GoalID)
	if err != nil {
		return err
	}
	if err = e.hydrate(&g); err != nil {
		return err
	}
	if g.State == GoalDone || g.State == GoalStopped {
		return errNoChange
	}
	actions, err := actionsOf(v)
	if err != nil {
		return err
	}
	changed := false
	for i := range g.Waits {
		w := &g.Waits[i]
		if !w.Recorded || w.Wake != s.Wake {
			continue
		}
		if s.TaskID != "" && w.TaskID != s.TaskID && w.TodoID != s.TaskID {
			continue
		}
		if g.Paused || readOpen(actions, g.ID, w.Wake) {
			if w.PendingSignal != s.ID {
				w.PendingSignal = s.ID
				changed = true
			}
			continue
		}
		if readSeen(actions, g.ID, w.Wake, "signal", s.ID) {
			continue
		}
		a := readAction(g, *w, "signal", s.ID, e.cfg.Now())
		if err = put(v, "action", a.ID, a); err != nil {
			return err
		}
		actions = append(actions, a)
		changed = true
	}
	if !changed {
		return errNoChange
	}
	return put(v, "goal", g.ID, g)
}

// finishFailedSourceRead turns a failed deadline read into the expiry row.
// The read did not show that the source had closed, so the wait must not stay
// open with its only deadline read already consumed.
func finishFailedSourceRead(g *Goal, a Action) error {
	if a.ReadCause != "deadline" {
		return nil
	}
	open := true
	return applyWaitRead(g, a, Observation{SourceOpen: &open})
}

func applyWaitRead(g *Goal, a Action, o Observation) error {
	if o.SourceOpen == nil || a.ReadCause != "signal" && a.ReadCause != "deadline" {
		return ErrInvalid
	}
	wake := strings.TrimPrefix(a.TaskID, "wait:")
	idx := -1
	for i := range g.Waits {
		if g.Waits[i].Recorded && g.Waits[i].Wake == wake {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ErrConflict
	}
	if *o.SourceOpen && a.ReadCause != "deadline" {
		return nil
	}
	w := g.Waits[idx]
	if *o.SourceOpen {
		if g.DecisionRow == "" {
			g.DecisionRow = expiryRow(w)
			g.AttentionID = "goal:" + g.ID + ":wait:" + w.Wake
		} else {
			// Independent expiries share the goal's row. Keep every proposal
			// and its draft; a later source must not replace an earlier one.
			g.DecisionRow += "\n\n" + expiryRow(w)
		}
		g.BlockedReason = g.DecisionRow
		g.State = GoalBlocked
	}
	queueClear(g, w)
	g.Waits = append(g.Waits[:idx], g.Waits[idx+1:]...)
	return nil
}

func (e *Engine) syncWaits(ctx context.Context) {
	if e.cfg.ArmWait == nil && e.cfg.LinkWaitingOn == nil {
		return
	}
	s, err := e.Snapshot(ctx)
	if err != nil {
		return
	}
	now := e.cfg.Now().Unix()
	for _, g := range s.Goals {
		if e.cfg.LinkWaitingOn != nil {
			live := map[string]bool{}
			for _, w := range g.Waits {
				if w.TodoID != "" {
					live[w.TodoID] = true
				}
			}
			for _, c := range g.ClearTodos {
				if live[c.TodoID] || e.cfg.LinkWaitingOn(ctx, g, Wait{TodoID: c.TodoID, Who: c.Who, Since: c.Since, NextCheck: c.NextCheck}) == nil {
					_ = e.dropClearTodo(ctx, g.ID, c.TodoID)
				}
			}
		}
		for _, w := range g.Waits {
			if !w.Recorded || !namedWait(w) {
				continue
			}
			if e.cfg.ArmWait != nil && !w.Armed && w.Until > now {
				if e.cfg.ArmWait(ctx, g, w) == nil {
					_ = e.markWait(ctx, g.ID, w.Wake, func(w *Wait) { w.Armed = true })
				}
			}
			if e.cfg.LinkWaitingOn != nil && w.TodoID != "" && !w.Linked {
				if e.cfg.LinkWaitingOn(ctx, g, w) == nil {
					_ = e.markWait(ctx, g.ID, w.Wake, func(w *Wait) { w.Linked = true })
				}
			}
		}
	}
}

func (e *Engine) dropClearTodo(ctx context.Context, goalID, todoID string) error {
	err := e.journal.Update(ctx, Transition{ID: "wait.clear:" + rand.Text(), Kind: "wait.cleared", At: e.cfg.Now().Unix()}, func(v *View) error {
		g, err := loadGoal(v, goalID)
		if err != nil {
			return err
		}
		next := make([]ClearedWait, 0, len(g.ClearTodos))
		found := false
		for _, id := range g.ClearTodos {
			if id.TodoID == todoID && !found {
				found = true
				continue
			}
			next = append(next, id)
		}
		if !found {
			return errNoChange
		}
		g.ClearTodos = next
		return put(v, "goal", g.ID, g)
	})
	if err == errNoChange {
		return nil
	}
	return err
}

func (e *Engine) markWait(ctx context.Context, goalID, wake string, mark func(*Wait)) error {
	err := e.journal.Update(ctx, Transition{ID: "wait.mark:" + rand.Text(), Kind: "wait.marked", At: e.cfg.Now().Unix()}, func(v *View) error {
		g, err := loadGoal(v, goalID)
		if err != nil {
			return err
		}
		found := false
		for i := range g.Waits {
			if g.Waits[i].Recorded && g.Waits[i].Wake == wake {
				mark(&g.Waits[i])
				found = true
			}
		}
		if !found {
			return errNoChange
		}
		return put(v, "goal", g.ID, g)
	})
	if err == errNoChange {
		return nil
	}
	return err
}
