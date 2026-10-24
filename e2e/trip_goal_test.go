// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

// Real Core owns the ledger, timers, restart, source reads and closure. Only
// the manager, calendar and mail service are fixtures. No external account is
// used. The send is explicitly authorized by the fixture's person at admission.
func TestM2RunToDoneTripGoal(t *testing.T) {
	for _, mode := range []string{"reply", "expired"} {
		t.Run(mode, func(t *testing.T) { runTripGoal(t, mode) })
	}
}

type tripWorld struct {
	mu       sync.Mutex
	records  map[string]json.RawMessage
	writes   map[string]int
	reads    map[string]int
	sources  map[string]int
	url      string
	root     string
	deadline int64
	updates  chan work.State
}

func (w *tripWorld) serve(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch r.Method {
	case http.MethodPut:
		body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
		if err != nil || !json.Valid(body) {
			rw.WriteHeader(http.StatusBadRequest)
			return
		}
		w.records[r.URL.Path] = body
		w.writes[r.URL.Path]++
	case http.MethodGet:
		w.reads[r.URL.Path]++
	default:
		rw.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if raw, ok := w.records[r.URL.Path]; ok {
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write(raw)
	} else {
		rw.WriteHeader(http.StatusNotFound)
	}
}

func (w *tripWorld) request(ctx context.Context, method, path, body string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, w.url+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("plugin %s: HTTP %d", path, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 4096))
}

func tripTask() work.Task {
	return work.Task{ID: "prepare", Kind: work.BackgroundStep, Done: "The itinerary and private calendar block are ready", Repository: "trip", Worker: "background", Stage: work.TaskReady,
		Resources:  work.Resources{CPU: 1, MemoryMB: 100, DiskMB: 100},
		Background: &work.BackgroundBrief{Goal: "Prepare the trip", Scope: "the itinerary and meeting", NotTouch: "other events", Done: "The itinerary and private calendar block are ready", Result: "document", Checks: []work.BackgroundCheck{{Name: "records", Kind: "tool_called"}}}}
}

func (w *tripWorld) Authorize(_ context.Context, a work.Action) error {
	if a.GoalID != "trip-goal" {
		return work.ErrInvalid
	}
	return nil
}

func (w *tripWorld) Observe(ctx context.Context, a work.Action) (work.Observation, error) {
	// A restarted read is safe; never reissue a calendar write or send.
	if a.Kind == work.ReadSource || a.Kind == work.CheckCriterion {
		return w.Execute(ctx, a)
	}
	return work.Observation{}, fmt.Errorf("unexpected uncertain trip effect %s", a.Kind)
}

func (w *tripWorld) Execute(ctx context.Context, a work.Action) (work.Observation, error) {
	o := work.Observation{Status: work.OutcomeSucceeded, EffectsSettled: true}
	switch a.Kind {
	case work.PlanGoal:
		o.Tasks = []work.Task{tripTask()}
		if a.RecordRevision == "" || len(a.OpenCriteria) > 0 {
			o.Wait = &work.Wait{Reason: "the host's reply", Wake: "host-reply", Source: "/reply", Until: w.deadline,
				Recommendation: "The host has not replied. I recommend continuing with the tentative time.", Draft: "Please confirm whether Thursday at ten works."}
			o.Waits = []work.Wait{{Reason: "the booking confirmation", Wake: "booking-reply", Source: "/booking", Until: w.deadline, Recommendation: "The booking is unconfirmed. I recommend keeping the tentative itinerary.", Draft: "Please confirm the booking."}}
		}
	case work.Assign:
		if a.TaskID != "prepare" {
			return o, work.ErrInvalid
		}
		for _, item := range []struct{ path, body string }{
			{"/calendar", `{"day":"Thursday","time":"10:00","place":"Office","attendees":[]}`},
			{"/sent", `{"thread":"host","body":"Does Thursday at ten work?"}`},
		} {
			if _, err := w.request(ctx, http.MethodPut, item.path, item.body); err != nil {
				return o, err
			}
		}
		if err := os.WriteFile(filepath.Join(w.root, "itinerary.txt"), []byte("Thursday, ten, Office. Meeting time is tentative until confirmed.\n"), 0600); err != nil {
			return o, err
		}
		o.Proof = work.Proof{Revision: "itinerary-v1", Ref: "itinerary", Passed: true}
	case work.ReadSource:
		w.mu.Lock()
		w.sources[a.WaitSource]++
		w.mu.Unlock()
		body, err := w.request(ctx, http.MethodGet, a.WaitSource, "")
		if err != nil {
			return o, err
		}
		var reply struct{ Received bool }
		if err = json.Unmarshal(body, &reply); err != nil {
			return o, err
		}
		open := !reply.Received
		o.SourceOpen = &open
	case work.Verify:
		body, err := os.ReadFile(filepath.Join(w.root, "itinerary.txt"))
		if err != nil || len(body) == 0 {
			return o, fmt.Errorf("missing itinerary: %v", err)
		}
		o.Proof = work.Proof{Revision: a.Revision, Ref: "itinerary", Passed: true}
	case work.CheckCriterion:
		if a.Check == nil {
			return o, work.ErrInvalid
		}
		collected := &work.CollectedEvidence{}
		if a.Check.Kind == work.ReadEvidence {
			body, err := w.request(ctx, http.MethodGet, a.Check.Target, "")
			if err != nil {
				return o, err
			}
			collected.Record = body
		} else {
			body, err := os.ReadFile(filepath.Join(w.root, "itinerary.txt"))
			if err != nil {
				return o, err
			}
			collected.Output = string(body)
		}
		o.Criterion = &work.CriterionEvidence{Criterion: a.CriterionID, GoalRevision: a.GoalRevision, Round: a.CheckRound, Target: a.Check.Target, Revision: a.Revision, Kind: a.Check.Kind, Collected: collected, Ref: "read:" + a.CriterionID, Passed: true}
	case work.Resume, work.InspectPR, work.Merge, work.RunQA, work.RepairConflict, work.RepairQA, work.Deploy, work.Observe, work.Decide, work.ContinueWorker:
		return o, fmt.Errorf("unexpected trip action %s", a.Kind)
	default:
		return o, fmt.Errorf("unexpected trip action %s", a.Kind)
	}
	return o, nil
}

func (w *tripWorld) RereadEvidence(ctx context.Context, a work.Action, _ work.CriterionEvidence) (work.CriterionEvidence, error) {
	o, err := w.Execute(ctx, a)
	if err != nil {
		return work.CriterionEvidence{}, err
	}
	return *o.Criterion, nil
}

func runTripGoal(t *testing.T, mode string) {
	root, err := os.MkdirTemp("", "lina-trip-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	if err = os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-work-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	w := &tripWorld{root: root, deadline: clock.Load() + 3600, updates: make(chan work.State, 1), writes: map[string]int{}, reads: map[string]int{}, sources: map[string]int{}, records: map[string]json.RawMessage{"/reply": []byte(`{"received":false}`), "/booking": []byte(`{"received":false}`)}}
	server := httptest.NewServer(http.HandlerFunc(w.serve))
	defer server.Close()
	w.url = server.URL
	var engine *work.Engine
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/client.sock"), Work: &boot.WorkConfig{
		Now:    func() time.Time { return time.Unix(clock.Load(), 0) },
		Driver: func(e *work.Engine) (work.Driver, error) { engine = e; return w, nil },
		MainHost: func(context.Context) (work.Node, error) {
			return work.Node{ID: "main-work-fixture", Connected: true, ObservedAt: clock.Load(), CPUCores: 2, CPULimit: 2, AvailableMemoryMB: 2000, MemoryLimitMB: 2000, AvailableDiskMB: 2000, DiskLimitMB: 2000, Workers: map[string]bool{"background": true}}, nil
		},
		Ready: func(ctx context.Context, e *work.Engine, person, _ string) error {
			s, err := e.Snapshot(ctx)
			if err != nil || len(s.Goals) != 0 {
				return err
			}
			agent, err := fixtureWorkOwner(ctx, root)
			if err != nil {
				return err
			}
			return e.Admit(ctx, work.Goal{ID: "trip-goal", Person: person, Agent: agent, Summary: "Prepare the trip", Done: "The itinerary, meeting and message are checked", NoServiceReason: "travel preparation", Scope: work.GoalScope{Repositories: []string{"trip"}, Includes: "the trip"}, Criteria: []work.Criterion{
				{ID: "calendar", Statement: "The private meeting block is confirmed", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "calendar.read", Target: "/calendar", Expected: []byte(`{"day":"Thursday","time":"10:00","place":"Office","attendees":[]}`)}},
				{ID: "sent", Statement: "The approved message is in sent mail", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "mail.read", Target: "/sent", Expected: []byte(`{"thread":"host","body":"Does Thursday at ten work?"}`)}},
				{ID: "document", Statement: "The itinerary is at its destination", Check: work.CriterionCheck{Kind: work.RunEvidence, Method: "file.read", Target: "itinerary"}},
				{ID: "reply", Statement: "The host has replied", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "mail.read", Target: "/reply", Expected: []byte(`{"received":true}`)}},
				{ID: "booking", Statement: "The booking is confirmed", Check: work.CriterionCheck{Kind: work.ReadEvidence, Method: "mail.read", Target: "/booking", Expected: []byte(`{"received":true}`)}},
			}})
		},
		Changed: func(_ context.Context, s work.State) error {
			select {
			case <-w.updates:
			default:
			}
			w.updates <- s
			return nil
		},
	}}
	var cancel context.CancelFunc
	var done chan error
	start := func() {
		// The previous Core has joined before restart. Discard its retained
		// projection so the next barrier observes the new epoch's engine.
		select {
		case <-w.updates:
		default:
		}
		ctx, stop := context.WithCancel(t.Context())
		cancel, done = stop, make(chan error, 1)
		go func(result chan<- error) { result <- boot.Run(ctx, cfg) }(done)
	}
	stop := func() {
		cancel()
		err := <-done
		cancel = nil
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if cancel != nil {
			cancel()
			<-done
		}
	})
	await := func(match func(work.Goal) bool) work.Goal {
		t.Helper()
		deadline := time.NewTimer(45 * time.Second)
		defer deadline.Stop()
		var last work.Goal
		for {
			select {
			case s := <-w.updates:
				for _, g := range s.Goals {
					last = g
					if match(g) {
						return g
					}
				}
			case err := <-done:
				cancel = nil
				t.Fatalf("Core stopped: %v", err)
			case <-deadline.C:
				t.Fatalf("trip did not settle: state=%s waits=%+v reason=%s", last.State, last.Waits, last.BlockedReason)
			}
		}
	}
	waiting := func(g work.Goal) bool {
		return len(g.Tasks) == 1 && g.Tasks[0].Stage == work.TaskDone && g.State == work.GoalWaiting && len(g.Waits) == 2 && g.Waits[0].Armed && g.Waits[1].Armed
	}
	start()
	await(waiting)
	stop()
	// Restart while waiting. Calendar and sent-mail writes must not repeat.
	start()
	await(waiting)
	w.mu.Lock()
	w.records["/reply"] = []byte(`{"received":true}`)
	w.mu.Unlock()
	signal := work.Signal{ID: "reply-arrived", GoalID: "trip-goal", Wake: "host-reply"}
	if err = engine.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	if err = engine.Signal(t.Context(), signal); err != nil {
		t.Fatal(err)
	}
	await(func(g work.Goal) bool {
		return g.State == work.GoalWaiting && len(g.Waits) == 1 && g.Waits[0].Wake == "booking-reply"
	})
	if mode == "reply" {
		w.mu.Lock()
		w.records["/booking"] = []byte(`{"received":true}`)
		w.mu.Unlock()
		if err = engine.Signal(t.Context(), work.Signal{ID: "booking-arrived", GoalID: "trip-goal", Wake: "booking-reply"}); err != nil {
			t.Fatal(err)
		}
	} else {
		stop()
		clock.Store(w.deadline)
		start()
		g := await(func(g work.Goal) bool { return g.State == work.GoalBlocked && g.DecisionRow != "" })
		if len(g.Checks) != 0 || len(g.Waits) != 0 || !strings.Contains(g.DecisionRow, "Please confirm") {
			t.Fatalf("expired wait did not become a draft row: %+v", g)
		}
		// The person's answer keeps the tentative meeting. It revises the
		// definition; it grants no permission to send the suggested follow-up.
		var criteria []work.Criterion
		for _, c := range g.Criteria {
			if strings.HasPrefix(c.ID, "code:") {
				continue
			}
			if c.ID == "booking" {
				c.Statement = "The booking remains tentative as requested"
				c.Check.Expected = []byte(`{"received":false}`)
			}
			criteria = append(criteria, c)
		}
		_, err = engine.Revise(t.Context(), g.ID, g.RecordRevision, g.Person, g.Agent, work.Definition{Outcome: g.Summary, Done: g.Done, Scope: g.Scope, Criteria: criteria, Assumptions: []string{"Keep the tentative booking without a follow-up."}})
		if err != nil {
			t.Fatal(err)
		}
	}
	g := await(func(g work.Goal) bool { return g.State == work.GoalDone })
	if len(g.Checks) != 6 {
		t.Fatalf("trip closed without every check: %+v", g.Checks)
	}
	for _, c := range g.Checks {
		if !c.Passed || c.Collected == nil {
			t.Fatalf("missing read-back: %+v", c)
		}
		if (c.Criterion == "calendar" || c.Criterion == "sent") && !bytes.Contains(c.Collected.Record, []byte("Thursday")) {
			t.Fatalf("external record was not collected: %+v", c)
		}
	}
	line, err := work.CloseLine(g)
	if err != nil || !strings.Contains(line, "The approved message is in sent mail") {
		t.Fatalf("summary lost evidence: %s %v", line, err)
	}
	stop()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writes["/calendar"] != 1 || w.writes["/sent"] != 1 || w.sources["/reply"] != 1 || w.sources["/booking"] != 1 || w.reads["/reply"] < 3 || w.reads["/booking"] < 3 || w.reads["/calendar"] < 2 || w.reads["/sent"] < 2 {
		t.Fatalf("effects repeated or read-back missing: writes=%v reads=%v", w.writes, w.reads)
	}
}
