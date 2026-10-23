// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

// TestM2PersonaGrowth (m2/persona-growth.json) uses a real Moirai worker
// process against Core's job socket with a synthetic precomputed growth
// candidate. It checks forged proposal refusal, next-turn persona application,
// a committed learning notice and one-action undo through the authorized Core
// tool registry, and suppression of an undone snapshot after reopening canon.
// The service harness uses the same private state, process deadlines and
// cleanup as the M1 worker scenario; it needs no model or personal persona
// text.
//
// #34's harness owns the private state, credential-free real worker process,
// socket readiness, deadlines and cleanup. The Core job and tool services are
// production services hosted here with synthetic, precomputed Lachesis input.
func TestM2PersonaGrowth(t *testing.T) {
	t.Parallel()

	var scenario struct {
		JobID     string                 `json:"job_id"`
		Initial   agents.Layers          `json:"initial"`
		Candidate agents.GrowthCandidate `json:"candidate"`
	}
	data, err := os.ReadFile("../m2/persona-growth.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildCommand(t, "moirai-worker"))
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	migrations := canon.Migrations()
	path := filepath.Join(h.root, "canon.sqlite")
	cfg := canon.Config{Path: path, Epoch: 1, Controls: ctl, Migrations: migrations}
	db, err := canon.Open(h.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	s, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	a, err := s.Create(h.ctx, agents.Create{Name: "fixture-agent", PersonID: "fixture-person", Layers: scenario.Initial})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.Prepare(h.ctx, "fixture-turn-1", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	var logicalTime atomic.Int64
	logicalTime.Store(time.Now().Add(-time.Hour).UnixNano())
	newService := func() *jobs.Service {
		t.Helper()
		definition, err := jobs.GrowthDefinition(s, a.ID, "fixture-person")
		if err != nil {
			t.Fatal(err)
		}
		service, err := jobs.New(jobs.Config{Store: db, Epoch: 1, Now: func() time.Time { return time.Unix(0, logicalTime.Load()) }, Definitions: map[string]jobs.Definition{scenario.JobID: definition}})
		if err != nil {
			t.Fatal(err)
		}
		return service
	}
	service := newService()
	if err = service.Register(h.ctx, jobapi.Job{ID: scenario.JobID, Kind: jobapi.Memory, Enabled: true, Deterministic: true, Every: time.Second, Timeout: operationTimeout, Adoption: "baseline"}); err != nil {
		t.Fatal(err)
	}
	input, _ := json.Marshal(scenario.Candidate)
	if err = service.Enqueue(h.ctx, scenario.JobID, jobapi.Task{Sequence: 1, Input: input}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(h.root, "run", "jobs.sock")
	stopService := startJobService(t, h.ctx, service, socket, ctl)
	runWorker := func() {
		t.Helper()
		p := h.command("--canon", path, "--socket", socket, "--once")
		p.start(t)
		p.wait(t, 0)
	}
	// Reject a worker attempting to substitute its own candidate or extra layers.
	client := jobapi.NewClient(socket)
	forged := scenario.Candidate
	forged.Snapshot = "fixture-forged"
	forgedData, _ := json.Marshal(forged)
	for _, output := range []json.RawMessage{forgedData, json.RawMessage(`{"id":"fixture-step","expected":1,"snapshot":"fixture-forged","notice":"fixture-notice","core":"fixture-forged"}`)} {
		if _, err = client.Apply(h.ctx, jobapi.Proposal{Epoch: 1, JobID: scenario.JobID, From: 0, Through: 1, Output: output}); !errors.Is(err, jobapi.ErrInvalid) {
			t.Fatalf("forged candidate: %v", err)
		}
	}
	runWorker()
	if got := jobWatermark(t, db, scenario.JobID); got != 1 {
		t.Fatalf("watermark=%d", got)
	}
	if pinned, err := s.Prepare(h.ctx, before.TurnID, a.ID); err != nil || pinned != before {
		t.Fatalf("pinned=%+v %v", pinned, err)
	}
	after, err := s.Prepare(h.ctx, "fixture-turn-2", a.ID)
	want := scenario.Initial
	want.GrowthSnapshot = scenario.Candidate.Snapshot
	if err != nil || after.Revision != 2 || after.Layers != want {
		t.Fatalf("applied=%+v %v", after, err)
	}
	call := growthTools(t, db, s, a.ID)
	result := call("notices", "growth_notices", struct{}{})
	var notices []agents.GrowthNotice
	if result.State != tools.Applied || json.Unmarshal(result.Body, &notices) != nil || len(notices) != 1 || notices[0].Notice != scenario.Candidate.Notice {
		t.Fatalf("notices=%s", result.Body)
	}
	// The person can dispatch the returned undo handle directly, in one action.
	undo := call("undo", "undo_growth", notices[0].Undo)
	if undo.State != tools.Applied {
		t.Fatalf("undo=%+v", undo)
	}
	replay := call("undo", "undo_growth", notices[0].Undo)
	if !reflect.DeepEqual(replay, undo) {
		t.Fatalf("replayed undo=%+v", replay)
	}
	if restored, err := s.Prepare(h.ctx, "fixture-turn-3", a.ID); err != nil || restored.Revision != 3 || restored.Layers != scenario.Initial || restored.Commit == before.Commit {
		t.Fatalf("restored=%+v %v", restored, err)
	}
	// Both the tool and job write paths bind the agent outside untrusted input.
	if invalid := call("foreign", "undo_growth", map[string]any{"revision": 2, "agent_id": "foreign"}); invalid.State == tools.Applied {
		t.Fatal("accepted foreign agent selector")
	}
	stopService()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(h.ctx); err != nil {
		t.Fatal(err)
	}
	db, err = canon.Open(h.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err = agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	service = newService()
	startJobService(t, h.ctx, service, socket, ctl)
	// A renamed candidate with the same undone snapshot is consumed silently,
	// so it cannot apply again or block subsequent background work on restart.
	scenario.Candidate.ID = "fixture-renamed"
	scenario.Candidate.Expected = 3
	input, _ = json.Marshal(scenario.Candidate)
	if err = service.Enqueue(h.ctx, scenario.JobID, jobapi.Task{Sequence: 2, Input: input}); err != nil {
		t.Fatal(err)
	}
	logicalTime.Add(int64(time.Second))
	runWorker()
	if got := jobWatermark(t, db, scenario.JobID); got != 2 {
		t.Fatalf("suppressed watermark=%d", got)
	}
	if current, err := s.Get(h.ctx, a.ID); err != nil || current.Revision != 3 {
		t.Fatalf("suppressed revision=%+v %v", current, err)
	}
	if notices, err := s.GrowthNotices(h.ctx, a.ID); err != nil || len(notices) != 0 {
		t.Fatalf("reproposed=%+v %v", notices, err)
	}
}

func growthTools(t *testing.T, db *canon.Store, s *agents.Store, agent string) func(string, string, any) tools.Result {
	t.Helper()
	auth := grant.Request{GrantID: "fixture-grant", MainID: "fixture-main", Epoch: 1, TaskID: "fixture-task", Host: "fixture-host", OSUser: "fixture-user", Person: "fixture-person", Agent: agent, Revision: "2"}
	target := "agent:" + agent
	service, err := authority.Open(t.Context(), db, authority.Config{Epoch: 1, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Hour), Scope: []grant.Permission{{Tool: "growth_notices", Target: target}, {Tool: "undo_growth", Target: target, Write: true}}, Revisions: map[string]string{target: auth.Revision}}, grant.Environment{Epoch: 1, Now: time.Now(), OSPermitted: true, Revision: auth.Revision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry(service, lifecycle.NewRegistry(nil))
	if _, err = coreexec.RegisterGrowth(registry, s, agent, auth.Person); err != nil {
		t.Fatal(err)
	}
	return func(key, name string, args any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(args)
		result, err := registry.Dispatch(t.Context(), tools.Call{Key: key, Name: name, Auth: auth, Args: raw})
		if err != nil && !errors.Is(err, tools.ErrInvalid) {
			t.Fatal(err)
		}
		return result
	}
}
