// SPDX-License-Identifier: Apache-2.0
package goal_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
)

// TestLiveStepStubWorker is the stub worker process of the live-session
// scenario. It announces itself, then waits until Core ends it.
func TestLiveStepStubWorker(t *testing.T) {
	if os.Getenv("LINA_LIVE_STEP_STUB") != "1" {
		return
	}
	fmt.Println("ready")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

// silentWorker has no process of its own: the scenario's stub process is the
// session, and the adapter only supplies the event stream the registry drains.
type silentWorker struct{ events chan worker.Event }

func newSilentWorker() *silentWorker { return &silentWorker{events: make(chan worker.Event)} }
func (*silentWorker) Name() string   { return "stub" }
func (*silentWorker) Version(context.Context) (string, error) {
	return "stub-1", nil
}
func (*silentWorker) Capabilities() worker.CapabilitySet { return nil }
func (*silentWorker) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	return worker.Thread{}, fmt.Errorf("stub worker is started by the scenario")
}
func (*silentWorker) Input(context.Context, worker.Thread, worker.Input) error {
	return fmt.Errorf("stub worker takes no input")
}
func (*silentWorker) Interrupt(context.Context, worker.Thread, worker.Reason) error {
	return fmt.Errorf("stub worker takes no interrupt")
}
func (w *silentWorker) Events() <-chan worker.Event { return w.events }
func (*silentWorker) Answer(context.Context, worker.Thread, worker.Answer) error {
	return fmt.Errorf("stub worker takes no answer")
}
func (*silentWorker) Quota(context.Context) ([]provider.QuotaRow, error) { return nil, nil }
func (*silentWorker) Snapshot(context.Context, worker.Thread) (worker.ThreadSnapshot, error) {
	return worker.ThreadSnapshot{}, fmt.Errorf("stub worker has no snapshot")
}

// stepWorld is the stub worker driver. Each step starts a real process and
// adopts it into Core's live registry, so the registry's count is the number of
// live worker processes.
type stepWorld struct {
	mu       sync.Mutex
	sessions *worker.Live
	root     string
	heads    map[string]string
	merges   map[string]string
	ends     int
}

func newStepWorld(root string, sessions *worker.Live) *stepWorld {
	return &stepWorld{sessions: sessions, root: root, heads: map[string]string{}, merges: map[string]string{}}
}

func (w *stepWorld) Authorize(_ context.Context, a work.Action) error {
	if a.GoalID != "ten-steps" {
		return work.ErrInvalid
	}
	return nil
}

func (w *stepWorld) Observe(context.Context, work.Action) (work.Observation, error) {
	return work.Observation{}, fmt.Errorf("unexpected uncertain effect")
}

func (w *stepWorld) proof(name, revision string) work.Proof {
	path := filepath.Join(w.root, name+".json")
	_ = os.WriteFile(path, []byte(`{"revision":"`+revision+`","passed":true}`), 0600)
	return work.Proof{Revision: revision, Ref: path, Passed: true}
}

func (w *stepWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	out := work.Observation{Status: work.OutcomeSucceeded}
	switch a.Kind {
	case work.Assign:
		return w.assign(a)
	case work.InspectPR:
		w.mu.Lock()
		head := w.heads[a.TaskID]
		w.mu.Unlock()
		if head == "" || head != a.Revision {
			return out, fmt.Errorf("inspection of an unknown head %q", a.Revision)
		}
		out.PR = work.PRState{Head: head, Mergeable: true, Checks: w.proof("ci-"+head, head), Review: w.proof("review-"+head, head), Verified: w.proof("manager-"+head, head)}
	case work.Merge:
		w.mu.Lock()
		head := w.heads[a.TaskID]
		if head != a.Revision {
			w.mu.Unlock()
			return out, fmt.Errorf("merge head moved")
		}
		merged := "merge-" + a.TaskID
		w.merges[a.TaskID] = merged
		w.mu.Unlock()
		out.Head = a.Revision
		out.Proof = w.proof("merge-"+a.TaskID, merged)
	case work.RunQA:
		w.mu.Lock()
		merged := w.merges[a.TaskID]
		w.mu.Unlock()
		if merged == "" || merged != a.Revision {
			return out, fmt.Errorf("QA was not pinned to a merge")
		}
		out.Proof = w.proof("qa-"+a.TaskID, a.Revision)
	case work.Verify:
		out.Proof = w.proof("runtime", a.Revision)
	case work.CheckCriterion:
		if a.Check == nil || a.Revision == "" {
			return out, fmt.Errorf("missing criterion check")
		}
		out.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Collected: &work.CollectedEvidence{Record: []byte(`{"observed":true}`), Output: "fixture check output"}, Ref: w.proof("criterion-"+a.ID, a.Revision).Ref, Passed: true}
	case work.Resume, work.RepairConflict, work.RepairQA, work.ContinueWorker, work.Deploy, work.Observe, work.Decide, work.PlanGoal:
		return out, fmt.Errorf("unexpected action %s", a.Kind)
	}
	return out, nil
}

// assign starts the step's stub process and hands it to Core. The step keeps it
// while its pull request is operated on; Core ends it when the step lands.
func (w *stepWorld) assign(a work.Action) (work.Observation, error) {
	out := work.Observation{Status: work.OutcomeSucceeded, EffectsSettled: true}
	cmd := exec.Command(os.Args[0], "-test.run=^TestLiveStepStubWorker$")
	cmd.Env = []string{"LINA_LIVE_STEP_STUB=1"}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return out, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return out, err
	}
	if err = cmd.Start(); err != nil {
		return out, err
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return out, fmt.Errorf("stub startup: %q %v", ready, err)
	}
	var once sync.Once
	closeProcess := func(context.Context) error {
		once.Do(func() {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			w.mu.Lock()
			w.ends++
			w.mu.Unlock()
		})
		return nil
	}
	thread := worker.Thread{ID: "thread-" + a.TaskID, TurnID: "turn-" + a.TaskID, TaskID: a.TaskID, AttemptID: "attempt-" + a.TaskID}
	session := worker.Session{ID: a.ID, Worker: "stub", Version: "stub-1", Started: time.Now(), Thread: thread}
	if err = w.sessions.Adopt(session, newSilentWorker(), closeProcess); err != nil {
		_ = closeProcess(context.Background())
		return out, err
	}
	w.mu.Lock()
	w.heads[a.TaskID] = "head-" + a.TaskID
	out.Head = w.heads[a.TaskID]
	w.mu.Unlock()
	// The observation handle names the session, as the dispatch service does.
	out.Handle = a.ID
	return out, nil
}

// liveSessionsScenario is the m3 fixture: the goal, its criterion and a
// ten-step graph in five dependent waves.
type liveSessionsScenario struct {
	Summary    string `json:"summary"`
	Done       string `json:"done"`
	Repository string `json:"repository"`
	Base       string `json:"base"`
	Worker     string `json:"worker"`
	Resources  struct {
		CPU      float64 `json:"cpu"`
		MemoryMB int64   `json:"memory_mb"`
		DiskMB   int64   `json:"disk_mb"`
	} `json:"resources"`
	Criterion struct {
		ID        string `json:"id"`
		Statement string `json:"statement"`
		Kind      string `json:"kind"`
		Method    string `json:"method"`
		Target    string `json:"target"`
	} `json:"criterion"`
	Steps []struct {
		ID        string   `json:"id"`
		Done      string   `json:"done"`
		BlockedBy []string `json:"blocked_by"`
	} `json:"steps"`
}

// readLiveSessions reads the scenario fixture. The graph is data, so the
// scenario states its waves without repeating them in code.
func readLiveSessions(t *testing.T) liveSessionsScenario {
	t.Helper()
	raw, err := os.ReadFile("../m3/live-sessions.json")
	if err != nil {
		t.Fatal(err)
	}
	var s liveSessionsScenario
	if err = json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Steps) != 10 {
		t.Fatalf("the scenario states %d steps, want 10", len(s.Steps))
	}
	return s
}

// tasks is the scenario's graph: each step of the fixture as a ready task.
func (s liveSessionsScenario) tasks() []work.Task {
	tasks := make([]work.Task, 0, len(s.Steps))
	for _, step := range s.Steps {
		tasks = append(tasks, work.Task{ID: step.ID, Done: step.Done, Repository: s.Repository, Base: s.Base, Area: step.ID + "/**", Worker: s.Worker, Resources: work.Resources{CPU: s.Resources.CPU, MemoryMB: s.Resources.MemoryMB, DiskMB: s.Resources.DiskMB}, Stage: work.TaskReady, DependsOn: step.BlockedBy})
	}
	return tasks
}

// criterion is the goal's done condition, read from the fixture.
func (s liveSessionsScenario) criterion(t *testing.T) work.Criterion {
	t.Helper()
	kinds := map[string]work.EvidenceKind{"read": work.ReadEvidence, "run": work.RunEvidence, "citation": work.CitationEvidence}
	kind, ok := kinds[s.Criterion.Kind]
	if !ok {
		t.Fatalf("unknown evidence kind %q", s.Criterion.Kind)
	}
	return work.Criterion{ID: s.Criterion.ID, Statement: s.Criterion.Statement, Check: work.CriterionCheck{Kind: kind, Method: s.Criterion.Method, Target: s.Criterion.Target}}
}

// expectedLive is the number of steps under pull-request operation, read from
// the published state alone. A step whose turn is still running is not counted:
// the stub worker finishes its turn before it reports, so its process belongs to
// the registry from that point on.
func expectedLive(s work.State) int {
	live := 0
	for _, g := range s.Goals {
		if g.State == work.GoalDone || g.State == work.GoalStopped {
			continue
		}
		for _, t := range g.Tasks {
			if t.ThreadHandle == "" {
				continue
			}
			switch t.Stage {
			case work.TaskDelivered, work.TaskNoReceipt:
				live++
			case work.TaskConflicted:
				if t.Conflicts < 1 {
					live++
				}
			case work.TaskWorkerLost:
				if t.EffectsSettled && t.Recoveries < 1 {
					live++
				}
			case work.TaskReady, work.TaskBuilding, work.TaskMerged, work.TaskQAFailed, work.TaskDone, work.TaskNeedsPerson:
			}
		}
	}
	return live
}

// building counts steps whose turn is still running. Their process exists but
// no handle is recorded yet, so they bound the live count without deciding it.
func undecided(s work.State) bool {
	for _, a := range s.Actions {
		switch a.State {
		case "queued", "claimed", "awaiting_authorization", "running", "unknown":
			return true
		}
	}
	return false
}

// inFlight counts committed dispatches whose process may exist without its
// delivery. They bound the live count without deciding it.
func inFlight(s work.State) int {
	count := 0
	for _, a := range s.Actions {
		switch a.State {
		case "claimed", "running", "unknown":
			count++
		}
	}
	return count
}

// TestLiveWorkerSessionsFollowTheStepFrontier (m3/live-sessions.json) runs a
// goal of ten steps in five dependent waves against a headless Core. Every
// step's worker is a real process that keeps running past its turn. After
// every committed event the registry's live count is compared with the
// projection of the published state: a step in progress or under pull-request
// operation counts, a landed step does not. Every ended thread is then read
// back from the registry.
func TestLiveWorkerSessionsFollowTheStepFrontier(t *testing.T) {
	scenario := readLiveSessions(t)
	root, err := os.MkdirTemp("", "lina-live-steps-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"live-main"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sessions := worker.NewLive()
	world := newStepWorld(t.TempDir(), sessions)
	closed := make(chan work.Goal, 1)
	var mu sync.Mutex
	events, maxLive, equal, leaks := 0, 0, 0, 0
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Driver:   func(*work.Engine) (work.Driver, error) { return world, nil },
		Sessions: sessions,
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "live-main", Connected: true, ObservedAt: time.Now().Unix(), CPUCores: 8, CPULimit: 8, AvailableMemoryMB: 16000, MemoryLimitMB: 16000, AvailableDiskMB: 16000, DiskLimitMB: 16000, Workers: map[string]bool{"stub": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, agent string) error {
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			saved, err := e.Snapshot(ctx)
			if err != nil || len(saved.Goals) > 0 {
				return err
			}
			return e.Admit(ctx, work.Goal{ID: "ten-steps", Person: person, Agent: agent, Summary: scenario.Summary, Done: scenario.Done, Scope: work.GoalScope{Repositories: []string{scenario.Repository}, Includes: scenario.Summary}, NoServiceReason: "library", Tasks: scenario.tasks(), Criteria: []work.Criterion{scenario.criterion(t)}})
		},
		Changed: func(_ context.Context, s work.State) error {
			mu.Lock()
			defer mu.Unlock()
			events++
			live := sessions.Count()
			if live > maxLive {
				maxLive = live
			}
			// A delivery commits its adoption in a worker goroutine, so a
			// dispatch that has not settled may still hold its process when
			// the next event publishes. Its step is in flight and bounds the
			// count; every settled event must match exactly.
			want := expectedLive(s)
			if live > want+inFlight(s) {
				leaks++
				t.Errorf("event %d: %d live worker processes for %d steps in progress or under pull-request operation and %d dispatches in flight", events, live, want, inFlight(s))
			}
			if !undecided(s) {
				if live != want {
					t.Errorf("event %d: %d live worker processes, want %d", events, live, want)
				} else {
					equal++
				}
			}
			for _, g := range s.Goals {
				if g.State == work.GoalDone {
					select {
					case closed <- g:
					default:
					}
				}
			}
			return nil
		},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- boot.Run(ctx, cfg) }()
	var g work.Goal
	select {
	case g = <-closed:
	case err := <-done:
		t.Fatalf("Core stopped: %v", err)
	case <-ctx.Done():
		t.Fatal("the ten-step goal did not finish")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if g.State != work.GoalDone || len(g.Tasks) != 10 {
		t.Fatalf("goal did not close all ten steps: %+v", g)
	}
	if equal == 0 || leaks != 0 {
		t.Fatalf("live sessions did not follow the frontier: comparable=%d leaks=%d", equal, leaks)
	}
	if maxLive < 2 {
		t.Fatalf("the graph never ran two steps at once: max live %d", maxLive)
	}
	// Every step's worker was live past its turn and then ended by Core.
	if world.ends != 10 || sessions.Count() != 0 {
		t.Fatalf("live sessions after landing: ended=%d live=%d", world.ends, sessions.Count())
	}
	handles := []string{}
	for _, task := range g.Tasks {
		if task.Session == "" || task.Stage != work.TaskDone {
			t.Fatalf("step %s did not record its session: %+v", task.ID, task)
		}
		ended, ok := sessions.Ended(task.Session)
		if !ok || ended.Thread.TaskID != task.ID || ended.Thread.ID == "" {
			t.Fatalf("ended thread of %s is not readable: %+v %v", task.ID, ended, ok)
		}
		handles = append(handles, ended.ID)
	}
	sort.Strings(handles)
	if len(handles) != 10 {
		t.Fatalf("ended threads = %v", handles)
	}
}
