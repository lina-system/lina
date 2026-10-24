// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work/goal"
)

// The adjust scenarios drive the work panel's own keys against installed Core's
// goal engine. A run limit is one of the person's choices, so Core grants it
// only to a decider outside its own tree: Core runs in a child process and the
// test, its TUI and its host report stay outside it. Every pass the checks read
// is Core's: the run replays the work ledger's committed events and never
// derives placement itself.

const (
	adjustRootEnv   = "LINA_WORK_PANEL_ADJUST_ROOT"
	adjustSocketEnv = "LINA_WORK_PANEL_ADJUST_SOCKET"
)

// adjustControl is how the parent steers the child's fixture: the host load it
// reports and the goals whose running step it releases.
type adjustControl struct {
	Load    float64  `json:"load"`
	Release []string `json:"release"`
}

func adjustControlPath(root string) string { return filepath.Join(root, "adjust", "control.json") }

func readAdjustControl(root string) adjustControl {
	raw, err := os.ReadFile(adjustControlPath(root))
	if err != nil {
		return adjustControl{}
	}
	var c adjustControl
	_ = json.Unmarshal(raw, &c)
	return c
}

func writeAdjustControl(t *testing.T, root string, c adjustControl) {
	t.Helper()
	dir := filepath.Join(root, "adjust")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(adjustControlPath(root), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

// adjustWorkers is the stub worker: it starts a step, holds it until the
// control file names its goal, then produces a real result. Core records the
// landing through its normal observation path.
type adjustWorkers struct {
	root     string
	mu       sync.Mutex
	held     map[string]chan struct{}
	starts   []string
	released map[string]bool
}

func newAdjustWorkers(root string) *adjustWorkers {
	return &adjustWorkers{root: root, held: map[string]chan struct{}{}, released: map[string]bool{}}
}

// adjustLog records one fixture step in the run's own file, so a stuck release
// is visible without guessing.
func adjustLog(root, line string) {
	dir := filepath.Join(root, "adjust")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "child.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format(time.RFC3339Nano), line)
}

func (w *adjustWorkers) Authorize(context.Context, goal.Action) error { return nil }

func (w *adjustWorkers) Observe(context.Context, goal.Action) (goal.Observation, error) {
	return goal.Observation{}, fmt.Errorf("unexpected worker observation")
}

func (w *adjustWorkers) Execute(ctx context.Context, a goal.Action) (goal.Observation, error) {
	if a.Kind != goal.Assign || a.StepKind != goal.BackgroundStep {
		<-ctx.Done()
		return goal.Observation{}, ctx.Err()
	}
	key := a.GoalID + "/" + a.TaskID
	hold := make(chan struct{})
	w.mu.Lock()
	w.held[key] = hold
	w.starts = append(w.starts, key)
	if w.released[a.GoalID] {
		close(hold)
	}
	w.mu.Unlock()
	adjustLog(w.root, "start "+key)
	select {
	case <-hold:
	case <-ctx.Done():
		return goal.Observation{}, ctx.Err()
	}
	adjustLog(w.root, "landed "+key)
	path := filepath.Join(os.TempDir(), "adjust-"+strings.ReplaceAll(key, "/", "-")+".txt")
	if err := os.WriteFile(path, []byte("step result verified\n"), 0600); err != nil {
		return goal.Observation{}, err
	}
	return goal.Observation{Status: goal.OutcomeSucceeded, EffectsSettled: true, Proof: goal.Proof{Revision: a.ID, Ref: path, Passed: true}}, nil
}

// release lets every step of the goal finish and keeps later steps of that goal
// from holding again, so the scenario can end its work on demand.
func (w *adjustWorkers) release(goalID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	adjustLog(w.root, "release "+goalID)
	w.released[goalID] = true
	for key, hold := range w.held {
		if !strings.HasPrefix(key, goalID+"/") {
			continue
		}
		select {
		case <-hold:
		default:
			close(hold)
		}
	}
}

// running names the steps the worker currently holds.
func (w *adjustWorkers) running() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string{}, w.starts...)
}

// adjustPass is one committed scheduling pass, replayed from the ledger.
type adjustPass struct {
	Seq         int64
	At          time.Time
	Rank        []string
	Running     []string
	Started     []string
	Reasons     map[string]string
	Places      map[string]int
	Limit       int
	Auto        bool
	LimitKey    string
	States      map[string]string
	Paused      map[string]bool
}

// adjustGoal is the goal projection after a pass.
type adjustGoal struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Rank        int    `json:"rank"`
	Paused      bool   `json:"paused"`
	QueuePlace  int    `json:"queue_place"`
	QueueReason string `json:"queue_reason"`
}

// adjustAction is the part of an action that holds a run slot.
type adjustAction struct {
	ID       string `json:"id"`
	GoalID   string `json:"goal_id"`
	TaskID   string `json:"task_id"`
	Kind     string `json:"kind"`
	StepKind string `json:"step_kind"`
	State    string `json:"state"`
}

// adjustLimit is the person's recorded run limit.
type adjustLimit struct {
	Automatic bool   `json:"automatic"`
	Value     int    `json:"value"`
	Current   int    `json:"current"`
	Reason    string `json:"reason"`
}

// adjustLedger replays the work ledger and yields one entry per committed
// scheduling pass, in commit order.
func adjustLedger(t *testing.T, ctx context.Context, root string) []adjustPass {
	t.Helper()
	// A read-only connection cannot see the writer's WAL pages, so the replay
	// reads through the same WAL the running Core appends to.
	db, err := canon.OpenSQLite(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT seq,kind,body FROM work_events ORDER BY seq")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	records := map[string]json.RawMessage{}
	var passes []adjustPass
	for rows.Next() {
		var seq int64
		var kind string
		var raw []byte
		if err = rows.Scan(&seq, &kind, &raw); err != nil {
			t.Fatal(err)
		}
		var entry struct {
			Event struct {
				RecordedAt string `json:"recorded_at"`
			} `json:"event"`
			Changes []struct {
				Kind string          `json:"kind"`
				ID   string          `json:"id"`
				Body json.RawMessage `json:"body"`
			} `json:"changes"`
		}
		if err = json.Unmarshal(raw, &entry); err != nil {
			t.Fatal(err)
		}
		started := []string{}
		changed := make([]struct {
			kind string
			body json.RawMessage
		}, 0, len(entry.Changes))
		for _, c := range entry.Changes {
			records[c.Kind+"\x00"+c.ID] = c.Body
			changed = append(changed, struct {
				kind string
				body json.RawMessage
			}{c.Kind, c.Body})
		}
		if kind != "work.derived" {
			continue
		}
		pass := adjustPass{Seq: seq, Reasons: map[string]string{}, Places: map[string]int{}, States: map[string]string{}, Paused: map[string]bool{}}
		if at, err := time.Parse(time.RFC3339Nano, entry.Event.RecordedAt); err == nil {
			pass.At = at
		}
		var goals []adjustGoal
		var actions []adjustAction
		var limit *adjustLimit
		for key, body := range records {
			kind, _, _ := strings.Cut(key, "\x00")
			switch kind {
			case "goal":
				var g adjustGoal
				if json.Unmarshal(body, &g) == nil {
					goals = append(goals, g)
				}
			case "action":
				var a adjustAction
				if json.Unmarshal(body, &a) == nil {
					actions = append(actions, a)
				}
			case "limit":
				var l adjustLimit
				if json.Unmarshal(body, &l) == nil {
					limit = &l
				}
			}
		}
		for _, c := range changed {
			if c.kind != "action" {
				continue
			}
			var a adjustAction
			// Only a dispatched step starts work: a check or a wait is not a
			// step of the 일.
			if json.Unmarshal(c.body, &a) == nil && a.State == "queued" && adjustReserves(a.Kind) && a.StepKind != "wait" {
				started = append(started, a.GoalID)
			}
		}
		sort.Slice(goals, func(i, j int) bool {
			if goals[i].Rank != goals[j].Rank {
				return goals[i].Rank < goals[j].Rank
			}
			return goals[i].ID < goals[j].ID
		})
		for _, g := range goals {
			pass.States[g.ID] = g.State
			pass.Paused[g.ID] = g.Paused
			if g.State == "done" || g.State == "stopped" {
				continue
			}
			pass.Rank = append(pass.Rank, g.ID)
			if g.QueuePlace > 0 {
				pass.Reasons[g.ID] = g.QueueReason
				pass.Places[g.ID] = g.QueuePlace
			}
		}
		holds := map[string]bool{}
		for _, a := range actions {
			if a.State == "finished" || a.StepKind == "wait" || !adjustReserves(a.Kind) {
				continue
			}
			holds[a.GoalID] = true
		}
		// Running is what held a slot before this pass placed anything, so a
		// pass is read against the steps that were already running.
		for _, g := range goals {
			if holds[g.ID] && !contains(started, g.ID) {
				pass.Running = append(pass.Running, g.ID)
			}
		}
		sort.Strings(pass.Running)
		if limit != nil {
			pass.Auto = limit.Automatic
			pass.Limit = limit.Current
			if pass.Limit == 0 {
				pass.Limit = limit.Value
			}
			pass.LimitKey = limit.Reason
		}
		pass.Started = started
		passes = append(passes, pass)
	}
	if err = rows.Err(); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	return passes
}

// adjustReserves reports an action that holds a run slot, as the engine counts
// it: a worker action that has not finished and is not a wait step.
func adjustReserves(kind string) bool {
	switch goal.ActionKind(kind) {
	case goal.Assign, goal.Resume, goal.RepairConflict, goal.RepairQA, goal.ContinueWorker:
		return true
	}
	return false
}

// TestM3WorkPanelAdjustChild runs the fixture Core for the adjust scenarios. It
// reports the host load the parent writes and releases the steps the parent
// names, so the parent observes placement without owning the engine.
func TestM3WorkPanelAdjustChild(t *testing.T) {
	root := os.Getenv(adjustRootEnv)
	if root == "" {
		return
	}
	ctx, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer cancel()
	workers := newAdjustWorkers(root)
	var mu sync.Mutex
	control := adjustControl{}
	// Core refuses a host report whose identity is not its own main.
	var main struct {
		MainID string `json:"main_id"`
	}
	raw, err := os.ReadFile(filepath.Join(root, "system/core/main.json"))
	if err != nil || json.Unmarshal(raw, &main) != nil || main.MainID == "" {
		t.Fatalf("main id: %v", err)
	}
	hostFor := func(load float64) goal.Node {
		return goal.Node{ID: main.MainID, Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 20, CPULimit: 20, LoadCPU: load, AvailableMemoryMB: 1 << 20, MemoryLimitMB: 1 << 20, AvailableDiskMB: 1 << 20, DiskLimitMB: 1 << 20, Workers: map[string]bool{"background": true}}
	}
	socket := os.Getenv(adjustSocketEnv)
	if socket == "" {
		socket = filepath.Join(root, "run/client.sock")
	}
	cfg := boot.Config{StateRoot: root, Socket: socket, Work: &boot.WorkConfig{
		Driver: func(e *goal.Engine) (goal.Driver, error) {
			go func() {
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
					}
					next := readAdjustControl(root)
					mu.Lock()
					changed := next.Load != control.Load
					control = next
					mu.Unlock()
					if changed {
						_ = e.NodeChanged(ctx, hostFor(next.Load))
					}
					for _, goalID := range next.Release {
						workers.release(goalID)
					}
				}
			}()
			return workers, nil
		},
		MainHost: func(context.Context) (goal.Node, error) {
			mu.Lock()
			defer mu.Unlock()
			return hostFor(control.Load), nil
		},
	}}
	if err := boot.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
