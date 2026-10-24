// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// RunLimit is how many goals of one person may have a step in progress at
// once. Automatic follows what the free slots and quota rows of Capacity carry;
// the person can set a number instead or return it to automatic.
type RunLimit struct {
	Automatic bool `json:"automatic,omitempty"`
	Value     int  `json:"value,omitempty"`
}

// MaxRunLimit bounds a person's number so a typo cannot outlast every report.
const MaxRunLimit = 1024

// RunLimitRecord is the durable limit of one person: the choice, the limit the
// last pass applied, and the reason it fell there.
type RunLimitRecord struct {
	RunLimit
	Current int    `json:"current,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// LimitState is the limit one derivation pass applied to one person.
type LimitState struct {
	Reason    string `json:"reason,omitempty"`
	Current   int    `json:"current"`
	Automatic bool   `json:"automatic,omitempty"`
}

// QueuePlace is a goal with a ready step and no run slot: its place in rank
// order and its one reason. Reason is slot, quota or load.
type QueuePlace struct {
	GoalID string `json:"goal_id"`
	Reason string `json:"reason"`
	Place  int    `json:"place"`
}

// Run limit reasons, as the queue and the line report them.
const (
	ReasonSlot  = "slot"
	ReasonQuota = "quota"
	ReasonLoad  = "load"
)

// stepResources is one goal's run slot. The limit counts goals, and a goal runs
// one step at a time, so a host carries as many goals as it has cores left after
// its load and cap; memory and disk are checked again by placement, where each
// step's own size is known.
var stepResources = Resources{CPU: 1, MemoryMB: 1, DiskMB: 1}

// CarriedSlots counts the steps the reports can carry at once, in goal units.
// Every eligible host offers the steps its measured cores, load and caps allow,
// whatever it currently reserves; a host offers nothing while every worker
// ready on it has a blocked quota row, because a step there could not start.
func CarriedSlots(nodes []Node, now time.Time) (slots int, quotaBlocked bool) {
	for _, n := range nodes {
		carried := Slots(n, nil, stepResources, now)
		if carried <= 0 {
			continue
		}
		usable := false
		for worker := range n.Workers {
			if n.QuotaReset[worker] <= now.Unix() {
				usable = true
				break
			}
		}
		if !usable {
			quotaBlocked = true
			continue
		}
		slots += carried
	}
	return slots, quotaBlocked
}

// runLimitFor is one pass's reading of a person's choice. Automatic takes the
// goals already running plus the additional goals the free slots and quota rows
// of Capacity can carry, never below one: a slot already held by a running step
// never lowers the limit while capacity remains, and when the reports can carry
// no additional goal the limit admits no new start and falls as the running
// steps end. The reason names the report that held it down, and a number the
// person set is always the run limit itself.
func runLimitFor(choice RunLimit, nodes []Node, now time.Time, running int) LimitState {
	if !choice.Automatic && choice.Value > 0 {
		return LimitState{Current: choice.Value, Reason: ReasonSlot}
	}
	carried, quotaBlocked := CarriedSlots(nodes, now)
	reason := ReasonLoad
	if quotaBlocked {
		reason = ReasonQuota
	}
	return LimitState{Current: max(running, carried, 1), Reason: reason, Automatic: true}
}

// inProgressGoals maps each person to the goals whose worker step already holds
// a run slot: a committed award, whether or not it has been claimed yet, so two
// passes never count the same goal twice.
func inProgressGoals(actions []Action, personOf map[string]string) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, a := range actions {
		if !reserves(a) || a.TaskID == "" {
			continue
		}
		person := personOf[a.GoalID]
		if person == "" {
			continue
		}
		if out[person] == nil {
			out[person] = map[string]bool{}
		}
		out[person][a.GoalID] = true
	}
	return out
}

// SetRunLimit records the person's choice as a durable command and reports the
// committed limit. It changes nothing else: the next derivation applies it.
func (e *Engine) SetRunLimit(ctx context.Context, person string, limit RunLimit, id string) (RunLimitRecord, bool, error) {
	if person == "" || id == "" || limit.Value < 0 || limit.Value > MaxRunLimit || limit.Value > 0 && limit.Automatic {
		return RunLimitRecord{}, false, ErrInvalid
	}
	if !limit.Automatic && limit.Value == 0 {
		return RunLimitRecord{}, false, ErrInvalid
	}
	var out RunLimitRecord
	changed := false
	err := e.journal.Update(ctx, Transition{ID: "run_limit.set:" + id, Kind: "run_limit.set", At: e.cfg.Now().Unix(), Data: encode(struct {
		Person string   `json:"person"`
		Limit  RunLimit `json:"limit"`
	}{person, limit})}, func(v *View) error {
		record := RunLimitRecord{RunLimit: limit}
		if r, ok := v.Get("limit", person); ok {
			var previous RunLimitRecord
			if err := decodeLimit(r, &previous); err != nil {
				return err
			}
			if previous.RunLimit == limit {
				out = previous
				return errNoChange
			}
			// The applied value stays until the next pass recomputes it.
			record.Current, record.Reason = previous.Current, previous.Reason
		}
		out, changed = record, true
		return put(v, "limit", person, record)
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	if err != nil {
		return RunLimitRecord{}, false, err
	}
	if !changed {
		// A resent command runs no callback; answer the committed record.
		err = e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
			if r, ok := readLimited(ctx, tx, person); ok {
				out = r
			}
			return nil
		})
	}
	return out, changed, err
}

// runLimit reads one person's durable limit. An absent record is automatic.
func (e *Engine) runLimit(ctx context.Context, person string) (RunLimit, error) {
	var out RunLimit
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		if r, ok := readLimited(ctx, tx, person); ok {
			out = r.RunLimit
		}
		return nil
	})
	return out, err
}

// CurrentRunLimit reports the limit the last pass applied, including the reason
// an automatic limit fell below what one host alone would carry.
func (e *Engine) CurrentRunLimit(ctx context.Context, person string) (RunLimitRecord, error) {
	var out RunLimitRecord
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		r, ok := readLimited(ctx, tx, person)
		if ok {
			out = r
		}
		return nil
	})
	return out, err
}

func decodeLimit(r Record, out *RunLimitRecord) error {
	if json.Unmarshal(r.Body, out) != nil {
		return ErrInvalid
	}
	return nil
}

// readLimited reads one person's limit record inside a transaction.
func readLimited(ctx context.Context, tx *canon.Tx, person string) (RunLimitRecord, bool) {
	var raw []byte
	if err := tx.QueryRowContext(ctx, "SELECT body FROM work_records WHERE kind='limit' AND id=?", person).Scan(&raw); err != nil {
		return RunLimitRecord{}, false
	}
	var out RunLimitRecord
	if json.Unmarshal(raw, &out) != nil {
		return RunLimitRecord{}, false
	}
	return out, true
}

// runLimitBlocks reports whether starting this award would put one more goal
// past the person's limit. A goal that already holds a slot never blocks
// itself, and a step that is only observed is not a start.
func runLimitBlocks(state State, person, goalID, actionID string, now time.Time) bool {
	if person == "" || goalID == "" {
		return false
	}
	personOf := make(map[string]string, len(state.Goals))
	for _, g := range state.Goals {
		personOf[g.ID] = g.Person
	}
	held := map[string]bool{}
	for _, a := range state.Actions {
		if a.ID == actionID || a.State == "finished" || a.TaskID == "" || !workerAction(a.Kind) || stepKind(a.StepKind) == WaitStep || personOf[a.GoalID] != person {
			continue
		}
		held[a.GoalID] = true
	}
	if held[goalID] {
		return false
	}
	return len(held) >= runLimitFor(state.Limits[person], state.Nodes, now, len(held)).Current
}
