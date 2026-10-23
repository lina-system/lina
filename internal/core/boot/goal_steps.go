// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/runs"
	goal "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Delegated managers have an internal conversation. Their background results
// return through the recorded originating conversation, with its own authority.
func (d *codeGoalDriver) goalRunOrigin(ctx context.Context, g goal.Goal) (runs.Origin, error) {
	o := runs.Origin{Person: g.Person, Agent: g.Agent, Conversation: g.Conversation, Turn: g.Turn, Epoch: d.runtime.epoch}
	err := d.runtime.db.Read(ctx, func(tx *canon.Tx) error {
		a, err := readCoding(ctx, tx, g.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if a.Owner != g.Agent || a.Conversation != g.Conversation || a.Origin.Person != g.Person {
			return goal.ErrNeedsPerson
		}
		o.Agent, o.Conversation, o.Turn = a.Origin.Agent, a.Origin.Conversation, a.Origin.Turn
		return nil
	})
	return o, err
}

func (d *codeGoalDriver) backgroundStep(ctx context.Context, g goal.Goal, t goal.Task, a goal.Action, observe bool) (goal.Observation, error) {
	if d.runtime.background == nil || t.Background == nil {
		return goal.Observation{}, goal.ErrNeedsPerson
	}
	s := d.runtime.background.service
	origin, err := d.goalRunOrigin(ctx, g)
	if err != nil {
		return goal.Observation{}, err
	}
	var id string
	if observe {
		r, found, err := s.Started(ctx, origin, a.ID)
		if err != nil {
			return goal.Observation{}, err
		}
		if !found {
			return goal.Observation{Status: goal.OutcomeUnknown}, nil
		}
		id = r.ID
	} else {
		raw, err := json.Marshal(t.Background)
		if err != nil {
			return goal.Observation{}, err
		}
		result := s.StartTool(origin).Execute(ctx, provider.ToolCall{ID: a.ID, Name: runs.StartToolName, Arguments: string(raw)})
		if result.Code != loop.OK {
			return goal.Observation{}, goal.ErrNeedsPerson
		}
		var receipt runs.Receipt
		if err = json.Unmarshal(result.Output, &receipt); err != nil {
			return goal.Observation{}, err
		}
		id = receipt.ID
	}
	// Release is idempotent; the durable native-call receipt prevents a second run.
	if err := s.Release(ctx, id); err != nil {
		return goal.Observation{}, err
	}
	for {
		changed := s.Changed()
		r, err := s.Get(ctx, id)
		if err != nil {
			return goal.Observation{}, err
		}
		if r.State == runs.Landed {
			if r.Outcome != runs.Complete || r.Material.ID == "" || r.Material.Revision < 1 {
				return goal.Observation{Status: goal.OutcomeNeedsPerson, Handle: id, EffectsSettled: true, Code: "background_incomplete"}, nil
			}
			return goal.Observation{Status: goal.OutcomeSucceeded, Handle: id, EffectsSettled: true, Proof: goal.Proof{Revision: fmt.Sprintf("%s:%d", r.Material.ID, r.Material.Revision), Ref: r.Material.ID, Passed: true}}, nil
		}
		select {
		case <-ctx.Done():
			return goal.Observation{}, ctx.Err()
		case <-changed:
		}
	}
}

// A wait holds no worker lease. Signals trigger an owner-scoped source read;
// dates and expiry use a timer, never a polling loop or a model call.
func (d *codeGoalDriver) waitStep(ctx context.Context, g goal.Goal, t goal.Task, a goal.Action) (goal.Observation, error) {
	w := t.Wait
	if w == nil {
		return goal.Observation{}, goal.ErrInvalid
	}
	deadline := w.At
	if w.RecordID != "" {
		deadline = w.Deadline
	}
	timer := time.NewTimer(max(0, time.Until(time.Unix(deadline, 0))))
	defer timer.Stop()
	for {
		changed := d.runtime.db.Changed()
		if w.RecordID != "" {
			row, err := d.runtime.records.ViewRecord(ctx, g.Person, w.RecordID)
			if err != nil {
				return goal.Observation{}, err
			}
			if row.Revision != w.Revision {
				return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: row.Revision, Ref: w.RecordID, Passed: true}}, nil
			}
		}
		if time.Now().Unix() >= deadline {
			if w.RecordID != "" {
				return goal.Observation{Status: goal.OutcomeNeedsPerson, EffectsSettled: true, Code: "wait_expired"}, nil
			}
			return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: fmt.Sprint(w.At), Ref: "date:" + a.ID, Passed: true}}, nil
		}
		select {
		case <-ctx.Done():
			return goal.Observation{}, ctx.Err()
		case <-timer.C:
		case <-changed:
		}
	}
}

// Verification reads the result's owner and revision again. It does not restart
// a run or reopen a date wait, and an expired or changed result cannot close a goal.
func (d *codeGoalDriver) readStepResult(ctx context.Context, s goal.State, g goal.Goal, t goal.Task) (goal.Proof, error) {
	if t.Stage != goal.TaskDone || !t.Result.Passed || t.Result.Ref == "" || t.Result.Revision == "" {
		return goal.Proof{}, goal.ErrNeedsPerson
	}
	switch t.Kind {
	case goal.BackgroundStep:
		if d.runtime.background == nil {
			return goal.Proof{}, goal.ErrNeedsPerson
		}
		origin, err := d.goalRunOrigin(ctx, g)
		if err != nil {
			return goal.Proof{}, err
		}
		for _, a := range s.Actions {
			if a.GoalID != g.ID || a.TaskID != t.ID || a.Kind != goal.Assign || a.Superseded || a.State != "finished" {
				continue
			}
			id := a.ID
			if a.ObserveActionID != "" {
				id = a.ObserveActionID
			}
			r, found, err := d.runtime.background.service.Started(ctx, origin, id)
			if err != nil {
				return goal.Proof{}, err
			}
			if found && r.State == runs.Landed && r.Outcome == runs.Complete && r.Material.ID == t.Result.Ref && fmt.Sprintf("%s:%d", r.Material.ID, r.Material.Revision) == t.Result.Revision {
				return t.Result, nil
			}
		}
	case goal.WaitStep:
		if t.Wait == nil {
			break
		}
		if t.Wait.At > 0 && time.Now().Unix() >= t.Wait.At && fmt.Sprint(t.Wait.At) == t.Result.Revision {
			return t.Result, nil
		}
		if t.Wait.RecordID != "" {
			row, err := d.runtime.records.ViewRecord(ctx, g.Person, t.Wait.RecordID)
			if err != nil {
				return goal.Proof{}, err
			}
			if row.Revision == t.Result.Revision && row.Revision != t.Wait.Revision {
				return t.Result, nil
			}
		}
	case goal.TaskStep, "":
	}
	return goal.Proof{}, goal.ErrNeedsPerson
}
