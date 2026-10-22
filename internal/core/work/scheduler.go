// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Schedule fixes the dispatch graph. Repository is a canonical repository
// identity shared by all clones; areas are repository-relative globs.
type Schedule struct {
	InputID    string   `json:"input_id,omitempty"`
	Group      string   `json:"group"`
	Repository string   `json:"repository"`
	Areas      []string `json:"areas"`
	DependsOn  []string `json:"depends_on"`
	Worker     string   `json:"worker"`
	Host       string   `json:"host"`
	Workdir    string   `json:"workdir,omitempty"`
	Packet     string   `json:"packet,omitempty"`
}
type ScheduleWait struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}
type PullRequest struct {
	Number      int    `json:"number"`
	Head        string `json:"head"`
	State       string `json:"state"`
	Mergeable   string `json:"mergeable,omitempty"`
	MergeCommit string `json:"merge_commit,omitempty"`
}
type Scheduling struct {
	Reserved        bool         `json:"reserved"`
	AreaHeld        bool         `json:"area_held"`
	CapacityHeld    bool         `json:"capacity_held"`
	Landed          bool         `json:"landed"`
	Closed          bool         `json:"closed"`
	ReturnRequested bool         `json:"return_requested"`
	ReturnSent      bool         `json:"return_sent"`
	ConflictHead    string       `json:"conflict_head,omitempty"`
	PRRun           string       `json:"pr_run,omitempty"`
	PR              *PullRequest `json:"pull_request,omitempty"`
}

// Hosts gives total schedulable slots (after external load/resource caps), not
// free slots. Missing hosts have no capacity. Unknown worker quota is usable.
type Availability struct {
	Hosts          map[string]int
	BlockedWorkers map[string]bool
}

func cardsIn(ctx context.Context, tx *canon.Tx) ([]Card, error) {
	rows, err := tx.QueryContext(ctx, "SELECT record FROM work_cards ORDER BY task_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cards []Card
	for rows.Next() {
		var raw []byte
		var c Card
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		cards = append(cards, c)
	}
	return cards, rows.Err()
}
func (s *Service) Cards(ctx context.Context) (cards []Card, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error { cards, err = cardsIn(ctx, tx); return err })
	return
}
func validateTask(t Task) error {
	if t.ID == "" || t.AgentID == "" || t.ConversationID == "" || strings.TrimSpace(t.Title) == "" || strings.TrimSpace(t.Done) == "" {
		return ErrInvalid
	}
	for _, root := range t.ArtifactRoots {
		if !absolute(root) {
			return ErrInvalid
		}
	}
	return nil
}
func validateGraph(cards []Card) error {
	tasks := map[string]Task{}
	for _, c := range cards {
		if c.Task.Schedule == nil {
			continue
		}
		t := c.Task
		p := t.Schedule
		if err := validateTask(t); err != nil {
			return err
		}
		if p.Group == "" || p.Repository == "" || p.Repository != strings.ToLower(p.Repository) || p.Worker == "" || p.Host == "" || len(p.Areas) > 64 || len(p.DependsOn) > 256 {
			return ErrInvalid
		}
		if _, err := AreasOverlap(p.Areas, p.Areas); err != nil {
			return err
		}
		if _, ok := tasks[t.ID]; ok {
			return ErrInvalid
		}
		tasks[t.ID] = t
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("%w: dependency cycle", ErrInvalid)
		}
		if visited[id] {
			return nil
		}
		t, ok := tasks[id]
		if !ok {
			return fmt.Errorf("%w: missing dependency", ErrInvalid)
		}
		visiting[id] = true
		deps := map[string]bool{}
		for _, dep := range t.Schedule.DependsOn {
			if deps[dep] {
				return ErrInvalid
			}
			deps[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	for id := range tasks {
		if err := visit(id); err != nil {
			return err
		}
	}
	var depends func(string, string) bool
	depends = func(id, ancestor string) bool {
		for _, dep := range tasks[id].Schedule.DependsOn {
			if dep == ancestor || depends(dep, ancestor) {
				return true
			}
		}
		return false
	}
	for i, a := range cards {
		if a.Task.Schedule == nil {
			continue
		}
		for _, b := range cards[i+1:] {
			if b.Task.Schedule == nil {
				continue
			}
			x, y := a.Task.Schedule, b.Task.Schedule
			if x.Group != y.Group || x.Repository != y.Repository {
				continue
			}
			overlap, err := AreasOverlap(x.Areas, y.Areas)
			if err != nil {
				return err
			}
			if overlap && !depends(a.Task.ID, b.Task.ID) && !depends(b.Task.ID, a.Task.ID) {
				return fmt.Errorf("%w: unordered write areas", ErrInvalid)
			}
		}
	}
	return nil
}

// Schedule admits the complete graph atomically. Existing identical tasks make
// a retried tool call harmless; task definitions cannot mutate under a lease.
func (s *Service) Schedule(ctx context.Context, tasks []Task) error {
	if len(tasks) == 0 || len(tasks) > 256 {
		return ErrInvalid
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		cards, err := cardsIn(ctx, tx)
		if err != nil {
			return err
		}
		old := map[string]Task{}
		for _, c := range cards {
			old[c.Task.ID] = c.Task
		}
		seen := map[string]bool{}
		for _, t := range tasks {
			if t.Schedule == nil || seen[t.ID] {
				return ErrInvalid
			}
			seen[t.ID] = true
			if prior, ok := old[t.ID]; ok {
				if !reflect.DeepEqual(prior, t) {
					return ErrState
				}
				continue
			}
			cards = append(cards, Card{Task: t})
		}
		if err = validateGraph(cards); err != nil {
			return err
		}
		for _, t := range tasks {
			if _, ok := old[t.ID]; ok {
				continue
			}
			if err = appendEvent(ctx, tx, Event{Key: "task:" + t.ID, TaskID: t.ID, Kind: "created", Task: &t}); err != nil {
				return err
			}
		}
		return nil
	})
}
func schedulingEvent(ctx context.Context, tx *canon.Tx, c Card, kind string, state Scheduling, reason *ScheduleWait) error {
	events, err := ledger(ctx, tx, c.Task.ID)
	if err != nil {
		return err
	}
	return appendEvent(ctx, tx, Event{Key: fmt.Sprintf("schedule:%s:%d", c.Task.ID, len(events)), TaskID: c.Task.ID, Kind: kind, Scheduling: &state, WaitReason: reason})
}
func (s *Service) Release(ctx context.Context, av Availability) (released []Card, err error) {
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		cards, e := cardsIn(ctx, tx)
		if e != nil {
			return e
		}
		index := map[string]Card{}
		used := map[string]int{}
		for _, c := range cards {
			index[c.Task.ID] = c
			if c.Task.Schedule != nil && c.Scheduling.CapacityHeld {
				used[c.Task.Schedule.Host]++
			}
		}
		for _, c := range cards {
			p := c.Task.Schedule
			if p == nil || c.Scheduling.Reserved || c.Scheduling.Closed || c.Scheduling.Landed || hasWait(c) {
				continue
			}
			var reason *ScheduleWait
			deps := append([]string(nil), p.DependsOn...)
			sort.Strings(deps)
			for _, dep := range deps {
				if !index[dep].Scheduling.Landed {
					reason = &ScheduleWait{"dependency", dep}
					break
				}
			}
			if reason == nil {
				for _, other := range cards {
					if other.Task.ID == c.Task.ID || other.Task.Schedule == nil || !other.Scheduling.AreaHeld || other.Task.Schedule.Repository != p.Repository {
						continue
					}
					overlap, e := AreasOverlap(p.Areas, other.Task.Schedule.Areas)
					if e != nil {
						return e
					}
					if overlap {
						reason = &ScheduleWait{"area", other.Task.ID}
						break
					}
				}
			}
			if reason == nil && av.BlockedWorkers[p.Worker] {
				reason = &ScheduleWait{"quota", p.Worker}
			}
			if reason == nil && used[p.Host] >= av.Hosts[p.Host] {
				reason = &ScheduleWait{"host_capacity", p.Host}
			}
			if reason != nil {
				if !reflect.DeepEqual(c.WaitReason, reason) {
					if e = schedulingEvent(ctx, tx, c, "schedule_wait", c.Scheduling, reason); e != nil {
						return e
					}
				}
				continue
			}
			state := c.Scheduling
			state.Reserved = true
			state.AreaHeld = true
			state.CapacityHeld = true
			if e = schedulingEvent(ctx, tx, c, "schedule_reserved", state, nil); e != nil {
				return e
			}
			c.Scheduling = state
			c.WaitReason = nil
			released = append(released, c)
			used[p.Host]++
			for i := range cards {
				if cards[i].Task.ID == c.Task.ID {
					cards[i] = c
					break
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return
}

// ObservePullRequest takes a Core-owned forge read, never a worker merge claim.
// Its run binding rejects stale completions and a merged record is monotonic.
func (s *Service) ObservePullRequest(ctx context.Context, id, run string, pr PullRequest) (request bool, err error) {
	if pr.Number <= 0 || pr.Head == "" || (pr.State != "OPEN" && pr.State != "CLOSED" && pr.State != "MERGED") || (pr.State == "MERGED" && pr.MergeCommit == "") {
		return false, ErrInvalid
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		es, e := ledger(ctx, tx, id)
		if e != nil {
			return e
		}
		c, e := Project(es)
		if e != nil {
			return e
		}
		if c.Task.Schedule == nil || c.Run == nil || c.Run.ID != run || c.Active {
			return ErrState
		}
		state := c.Scheduling
		if state.Landed || state.Closed {
			if reflect.DeepEqual(state.PR, &pr) {
				return nil
			}
			return ErrState
		}
		if state.PR != nil && state.PR.Number != pr.Number {
			return ErrState
		}
		if reflect.DeepEqual(state.PR, &pr) && state.PRRun == run {
			return nil
		}
		state.PR = &pr
		state.PRRun = run
		kind := "schedule_observed"
		switch pr.State {
		case "MERGED":
			state.Landed = true
			state.AreaHeld = false
			state.CapacityHeld = false
		case "CLOSED":
			state.Closed = true
			state.AreaHeld = false
			state.CapacityHeld = false
		case "OPEN":
			if pr.Mergeable == "CONFLICTING" {
				if !state.ReturnRequested {
					state.ReturnRequested = true
					state.ConflictHead = pr.Head
					request = true
				} else if state.ConflictHead != pr.Head || state.ReturnSent && c.Run.Generation > 1 {
					kind = "schedule_conflict"
				}
			}
		}
		return schedulingEvent(ctx, tx, c, kind, state, nil)
	})
	return
}

// ClaimReturn reserves the sole conflict correction before worker input. A
// crash after this boundary leaves an unknown effect, not permission to resend.
func (s *Service) ClaimReturn(ctx context.Context, id string, av Availability) (claimed bool, err error) {
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		es, err := ledger(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := Project(es)
		if err != nil {
			return err
		}
		if !c.Scheduling.ReturnRequested || c.Scheduling.ReturnSent || c.Active || c.Scheduling.Landed || c.Scheduling.Closed {
			return ErrState
		}
		cards, e := cardsIn(ctx, tx)
		if e != nil {
			return e
		}
		used := 0
		for _, other := range cards {
			if other.Task.Schedule != nil && other.Task.Schedule.Host == c.Task.Schedule.Host && other.Scheduling.CapacityHeld {
				used++
			}
		}
		var reason *ScheduleWait
		if av.BlockedWorkers[c.Task.Schedule.Worker] {
			reason = &ScheduleWait{"quota", c.Task.Schedule.Worker}
		} else if used >= av.Hosts[c.Task.Schedule.Host] {
			reason = &ScheduleWait{"host_capacity", c.Task.Schedule.Host}
		}
		if reason != nil {
			if reflect.DeepEqual(c.WaitReason, reason) {
				return nil
			}
			return schedulingEvent(ctx, tx, c, "schedule_wait", c.Scheduling, reason)
		}
		claimed = true
		state := c.Scheduling
		state.ReturnSent = true
		state.CapacityHeld = true
		return schedulingEvent(ctx, tx, c, "schedule_return", state, nil)
	})
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// WorkerInputNeeded preserves execution and area reservations while a worker's
// approval/question awaits the originating conversation.
func (s *Service) WorkerInputNeeded(ctx context.Context, id, run string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		es, err := ledger(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := Project(es)
		if err != nil {
			return err
		}
		if c.Run == nil || c.Run.ID != run || !c.Active {
			return ErrState
		}
		return schedulingEvent(ctx, tx, c, "schedule_input", c.Scheduling, nil)
	})
}

// UnknownExecution is an observation gap, not a terminal worker result.
func (s *Service) UnknownExecution(ctx context.Context, id, run string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		events, err := ledger(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := Project(events)
		if err != nil {
			return err
		}
		if c.Run == nil || c.Run.ID != run || !c.Active {
			return ErrState
		}
		return appendEvent(ctx, tx, Event{Key: "worker-unknown:" + run, TaskID: id, Kind: "unknown"})
	})
}

func (s *Service) WorkerInputAnswered(ctx context.Context, id, run string) error {
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		events, err := ledger(ctx, tx, id)
		if err != nil {
			return err
		}
		c, err := Project(events)
		if err != nil {
			return err
		}
		if c.Run == nil || c.Run.ID != run || !c.Active {
			return ErrState
		}
		return schedulingEvent(ctx, tx, c, "schedule_answered", c.Scheduling, nil)
	})
}
