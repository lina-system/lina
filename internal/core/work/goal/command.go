// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Command is a person's choice about one goal. Core records it and applies it
// on the next derivation; no command dispatches work by itself.
type Command string

const (
	CommandRankUp   Command = "rank_up"
	CommandRankDown Command = "rank_down"
	CommandPause    Command = "pause"
	CommandResume   Command = "resume"
	CommandStop     Command = "stop"
)

func (c Command) valid() bool {
	switch c {
	case CommandRankUp, CommandRankDown, CommandPause, CommandResume, CommandStop:
		return true
	}
	return false
}

// GoalCommand carries the authenticated person, the goal and the command
// identity of one recorded choice. ID is the input message identity, so a
// resent choice keeps one ledger event.
type GoalCommand struct {
	ID        string
	Goal      string
	Person    string
	Command   Command
	Confirmed bool
}

// ErrNotFound reports a goal that does not exist or belongs to another person.
var ErrNotFound = errors.New("work_not_found")

// Command journals the person's choice and applies it to the goal projection in
// the same transaction. The caller derives afterwards; a rank or pause never
// starts, stops or observes an effect itself. It reports whether the choice
// changed the goal: a choice that changes nothing records no command, and a
// resent message keeps its first one.
func (e *Engine) Command(ctx context.Context, c GoalCommand) (Goal, bool, error) {
	if c.ID == "" || c.Goal == "" || c.Person == "" || !c.Command.valid() {
		return Goal{}, false, ErrInvalid
	}
	if c.Command == CommandStop && !c.Confirmed {
		return Goal{}, false, ErrInvalid
	}
	var out Goal
	changed := false
	event := Transition{ID: "goal.command:" + c.ID, Kind: "goal.command", At: e.cfg.Now().Unix(), Data: encode(struct {
		Goal      string  `json:"goal"`
		Person    string  `json:"person"`
		Command   Command `json:"command"`
		Confirmed bool    `json:"confirmed"`
	}{c.Goal, c.Person, c.Command, c.Confirmed})}
	// A resent message keeps the command it committed: its original instant and
	// its one ledger record. A different payload under the same identity is
	// refused rather than overwriting the first choice.
	if committed, ok, err := e.journal.Committed(ctx, event.ID); err != nil {
		return Goal{}, false, err
	} else if ok {
		if !bytes.Equal(committed.Data, event.Data) {
			return Goal{}, false, ErrConflict
		}
		g, err := e.readGoal(ctx, c.Goal)
		return g, true, err
	}
	err := e.journal.Update(ctx, event, func(v *View) error {
		r, ok := v.Get("goal", c.Goal)
		if !ok {
			return ErrNotFound
		}
		var g Goal
		if err := decodeGoal(r, &g); err != nil {
			return err
		}
		if g.Person != c.Person {
			return ErrNotFound
		}
		final := g.State == GoalDone || g.State == GoalStopped
		switch c.Command {
		case CommandStop:
			if g.State == GoalStopped {
				out = g
				return errNoChange
			}
			if g.State == GoalDone {
				return ErrConflict
			}
			g.State = GoalStopped
			g.Paused = false
			g.Waits = nil
			g.AttentionID = ""
			g.BlockedReason = ""
			// Undispatched work never runs for a stopped goal, and its award must
			// not keep a node slot reserved. A claimed effect settles by reading
			// its outcome, so only queued work and an authority wait retire here.
			if err := retireStoppedActions(v, &g); err != nil {
				return err
			}
		case CommandPause, CommandResume:
			if final {
				return ErrConflict
			}
			if g.Paused == (c.Command == CommandPause) {
				out = g
				return errNoChange
			}
			g.Paused = c.Command == CommandPause
		case CommandRankUp, CommandRankDown:
			if final {
				return ErrConflict
			}
			ordered, err := normalizeRanks(v, c.Person)
			if err != nil {
				return err
			}
			moved, err := swapRank(v, ordered, c.Goal, c.Command)
			if err != nil {
				return err
			}
			if !moved && !hasChanges(v) {
				out = g
				return errNoChange
			}
			// normalizeRanks wrote every record it changed; read the result back.
			if r, ok = v.Get("goal", c.Goal); !ok {
				return ErrNotFound
			}
			if err = decodeGoal(r, &g); err != nil {
				return err
			}
		}
		out, changed = g, true
		return put(v, "goal", g.ID, g)
	})
	if errors.Is(err, errNoChange) {
		err = nil
	}
	if err != nil {
		return Goal{}, false, err
	}
	if out.ID == "" {
		// A no-op transition runs no callback; answer with the committed goal.
		g, err := e.readGoal(ctx, c.Goal)
		if err != nil {
			return Goal{}, false, err
		}
		out = g
	}
	return out, changed, nil
}

func (e *Engine) readGoal(ctx context.Context, id string) (Goal, error) {
	var g Goal
	err := e.cfg.DB.Read(ctx, func(tx *canon.Tx) error {
		v, err := readView(ctx, tx)
		if err != nil {
			return err
		}
		r, ok := v.Get("goal", id)
		if !ok {
			return ErrNotFound
		}
		return decodeGoal(r, &g)
	})
	return g, err
}

func decodeGoal(r Record, g *Goal) error {
	if err := json.Unmarshal(r.Body, g); err != nil {
		return ErrInvalid
	}
	return nil
}

// normalizeRanks gives every non-final goal of the person a dense rank in its
// current order. Goals written before ranks existed sort first, because a new
// goal always takes the last rank.
func normalizeRanks(v *View, person string) ([]Goal, error) {
	goals := []Goal{}
	for _, r := range v.Records("goal") {
		var g Goal
		if err := decodeGoal(r, &g); err != nil {
			return nil, err
		}
		if g.Person != person || g.State == GoalDone || g.State == GoalStopped {
			continue
		}
		goals = append(goals, g)
	}
	sort.SliceStable(goals, func(i, j int) bool {
		a, b := goals[i], goals[j]
		if (a.Rank > 0) != (b.Rank > 0) {
			return a.Rank <= 0
		}
		if a.Rank != b.Rank {
			return a.Rank < b.Rank
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt < b.CreatedAt
		}
		return a.ID < b.ID
	})
	for i := range goals {
		if goals[i].Rank == i+1 {
			continue
		}
		goals[i].Rank = i + 1
		if err := put(v, "goal", goals[i].ID, goals[i]); err != nil {
			return nil, err
		}
	}
	return goals, nil
}

// swapRank exchanges a goal's rank with its neighbour. It reports whether the
// pair moved; at the first or last place the order is unchanged.
func swapRank(v *View, ordered []Goal, id string, command Command) (bool, error) {
	index := -1
	for i := range ordered {
		if ordered[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return false, ErrNotFound
	}
	neighbour := index - 1
	if command == CommandRankDown {
		neighbour = index + 1
	}
	if neighbour < 0 || neighbour >= len(ordered) {
		return false, nil
	}
	a, b := ordered[index], ordered[neighbour]
	a.Rank, b.Rank = b.Rank, a.Rank
	if err := put(v, "goal", a.ID, a); err != nil {
		return false, err
	}
	return true, put(v, "goal", b.ID, b)
}

func hasChanges(v *View) bool { return len(v.changes) > 0 }

// retireStoppedActions closes every undispatched award of a stopped goal in the
// same transaction, so its reservations are released with the stop. A claimed
// action keeps its receipt and reservation until its effect is observed.
func retireStoppedActions(v *View, g *Goal) error {
	for _, r := range v.Records("action") {
		var a Action
		if err := json.Unmarshal(r.Body, &a); err != nil {
			return err
		}
		if a.GoalID != g.ID || a.ObserveActionID != "" || a.State != "queued" && a.State != "awaiting_authorization" {
			continue
		}
		restoreUndispatchedTask(g, a)
		a.State = "finished"
		a.Superseded, a.RetirementReconciled = true, true
		if err := put(v, "action", a.ID, a); err != nil {
			return err
		}
	}
	return nil
}
