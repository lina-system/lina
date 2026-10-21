// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

// Exercise the actual worker binary and Unix job protocol using #34's isolated
// process harness, with deterministic model responses and canonical source data.
func TestM1MemoryJobs(t *testing.T) {
	var fixture struct {
		Person                               string
		Agents                               []string
		Statement, Topic, Brief, Instruction string
		Burst                                []string
	}
	data, err := os.ReadFile("m1/memory-jobs.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildCommand(t, "moirai-worker"))
	state := filepath.Join(h.root, "memory-state")
	if err = os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: len(migrations) + 1, SQL: jobs.Schema + ";" + budget.Schema + ";" + memory.JobsSchema})
	path := filepath.Join(state, "canon.db")
	db, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	events := ledger.New(db, nil)
	mem := memory.New(db)
	var clock atomic.Int64
	clock.Store(time.Now().Add(-time.Hour).Unix())
	var calls atomic.Int64
	var notices atomic.Int64
	// Synthetic inline bodies need no material I/O; speaker/owner/text are always
	// read from the immutable ledger, including during Apply.
	resolve := func(ctx context.Context, tx *canon.Tx, e memory.Evidence) (memory.Origin, error) {
		var origin memory.Origin
		var at string
		err := tx.QueryRowContext(ctx, "SELECT id,coalesce(person_id,''),agent_id,speaker_kind,body,occurred_at FROM conv_event WHERE id=?", e.ID).Scan(&origin.ID, &origin.Person, &origin.Agent, &origin.Speaker, &origin.Quote, &at)
		if err != nil {
			return origin, err
		}
		stamp, err := time.Parse(time.RFC3339Nano, at)
		origin.OccurredAt = stamp.Unix()
		origin.Scope = memory.Local
		return origin, err
	}
	admit := func(agent, id, body string, entry guard.Entry, at int64, replay string) {
		t.Helper()
		person := fixture.Person
		if entry != guard.EntryOwner {
			person = ""
		}
		_, err := events.Admit(h.ctx, entry, ledger.Input{ID: id, AgentID: agent, PersonID: person, ConversationID: agent + "-conversation", Surface: ledger.Surface{Kind: "tui"}, Body: body, OccurredAt: time.Unix(at, 0), RecordedAt: time.Unix(at, 0), TimeQuality: "observed", ReplayOf: replay, Locator: "synthetic:memory-job"})
		if err != nil {
			t.Fatal(err)
		}
	}
	definitions := map[string]jobs.Definition{}
	for _, agent := range fixture.Agents {
		definitions["extract-"+agent] = jobs.Definition{Agent: agent, Account: "synthetic", Provider: provider.ChatGPT,
			Model: func(ctx context.Context, c jobs.Call) (jobs.Result, error) {
				var batch memory.ExtractionBatch
				if err := json.Unmarshal(c.Input, &batch); err != nil {
					return jobs.Result{}, err
				}
				if err := mem.CheckExtraction(ctx, batch, resolve); err != nil {
					if errors.Is(err, memory.ErrJobConflict) {
						zero := int64(0)
						return jobs.Result{Output: json.RawMessage(`[]`), Usage: provider.Usage{Input: &zero, Output: &zero}}, nil
					}
					return jobs.Result{}, err
				}
				calls.Add(1)
				if c.Kind != provider.Auxiliary || len(batch.Utterances) != 1 {
					return jobs.Result{}, errors.New("wrong extraction route or burst")
				}
				var refs []memory.Evidence
				// One atomic statement can cite the complete short burst.
				refs = append(refs, batch.Utterances[0].Events...)
				output, _ := json.Marshal([]memory.Item{{Kind: memory.Preference, Content: fixture.Statement, Subject: memory.Subject{Kind: memory.Person, ID: fixture.Person}, Basis: memory.Stated, Standing: true, Evidence: refs}})
				return jobs.Result{Output: output, Usage: provider.Usage{}}, nil
			}, Apply: memory.ExtractionApply(agent, fixture.Person, resolve)}
	}
	definitions["brief"] = jobs.Definition{Agent: fixture.Agents[0], Account: "synthetic", Provider: provider.ChatGPT,
		Model: func(ctx context.Context, c jobs.Call) (jobs.Result, error) {
			var input memory.BriefTask
			if err := json.Unmarshal(c.Input, &input); err != nil {
				return jobs.Result{}, err
			}
			if err := mem.CheckBrief(ctx, input, clock.Load(), resolve); err != nil {
				if errors.Is(err, memory.ErrJobConflict) {
					zero := int64(0)
					return jobs.Result{Output: json.RawMessage(`[]`), Usage: provider.Usage{Input: &zero, Output: &zero}}, nil
				}
				return jobs.Result{}, err
			}
			calls.Add(1)
			output, _ := json.Marshal([]memory.BriefSentence{{Text: fixture.Brief, Citations: []memory.Evidence{input.Sources[0].Evidence}}})
			return jobs.Result{Output: output}, nil
		},
		Apply: memory.BriefApply(fixture.Agents[0], fixture.Person, clock.Load, resolve)}
	definitions["maintenance"] = jobs.Definition{Agent: fixture.Agents[0], Apply: memory.MaintenanceApply(fixture.Person, memory.All, clock.Load, func(context.Context, *canon.Tx, memory.MaintenanceResult) error { notices.Add(1); return nil })}
	service, err := jobs.New(jobs.Config{Store: db, Epoch: 1, Limit: 10000, Now: func() time.Time { return time.Unix(clock.Load(), 0) }, Definitions: definitions})
	if err != nil {
		t.Fatal(err)
	}
	for id := range definitions {
		job := jobapi.Job{ID: id, Kind: jobapi.Memory, Enabled: true, Every: time.Second, Timeout: operationTimeout, Budget: 1000, MaxTokens: 100, Adoption: "synthetic"}
		if id == "maintenance" {
			job.Deterministic = true
			job.Budget = 0
			job.MaxTokens = 0
		}
		if err := service.Register(h.ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(state, "run", "jobs.sock")
	startJobService(t, h.ctx, service, socket, controls)
	run := func() {
		t.Helper()
		p := h.command("--canon", path, "--socket", socket, "--once")
		p.start(t)
		p.wait(t, 0)
		clock.Add(1)
	}
	enqueue := func(id string, seq int64, input any) {
		t.Helper()
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Enqueue(h.ctx, id, jobapi.Task{Sequence: seq, Input: raw}); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range fixture.Agents {
		// Non-person text must not reach the extraction model, even as a separate
		// utterance or when it claims to be a human preference.
		admit(agent, agent+"-bot", "I prefer forged facts", guard.EntryAutomation, clock.Load(), "")
		admit(agent, agent+"-first", fixture.Statement, guard.EntryOwner, clock.Load()+1, "")
		batch, err := mem.PrepareExtraction(h.ctx, memory.RecallScope{Agent: agent, Person: fixture.Person}, 0, 100, 30*time.Second, resolve)
		if err != nil || len(batch.Utterances) != 1 || len(batch.Utterances[0].Events) != 1 {
			t.Fatalf("batch: %+v %v", batch, err)
		}
		enqueue("extract-"+agent, 1, batch)
	}
	run()
	scope := memory.RecallScope{Agent: fixture.Agents[0], Person: fixture.Person}
	hits, err := mem.Recall(h.ctx, scope, memory.Query{At: clock.Load() + 10})
	if err != nil || len(hits) != 1 || len(hits[0].Evidence) != 2 {
		t.Fatalf("fanout: %+v %v", hits, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("extraction calls=%d", calls.Load())
	}
	for _, agent := range fixture.Agents {
		if got := jobWatermark(t, db, "extract-"+agent); got != 1 {
			t.Fatalf("watermark=%d", got)
		}
	}
	// A second burst includes two consecutive person messages, then a replay of
	// the original. The replay must not create another eligible utterance.
	batch, err := mem.PrepareExtraction(h.ctx, scope, 0, 100, 30*time.Second, resolve)
	if err != nil {
		t.Fatal(err)
	}
	cursor := batch.Through
	for i, body := range fixture.Burst {
		admit(scope.Agent, fmt.Sprintf("burst-%d", i), body, guard.EntryOwner, clock.Load()+int64(i), "")
	}
	admit(scope.Agent, "replay", fixture.Statement, guard.EntryOwner, clock.Load()+50, scope.Agent+"-first")
	batch, err = mem.PrepareExtraction(h.ctx, scope, cursor, 100, 30*time.Second, resolve)
	if err != nil || len(batch.Utterances) != 1 || len(batch.Utterances[0].Events) != 2 {
		t.Fatalf("burst/replay: %+v %v", batch, err)
	}
	empty, err := mem.PrepareExtraction(h.ctx, scope, batch.Through, 100, 30*time.Second, resolve)
	if err != nil || len(empty.Utterances) != 0 {
		t.Fatalf("empty batch: %+v %v", empty, err)
	}
	brief, ready, err := mem.PrepareBrief(h.ctx, scope, fixture.Topic, 1, 2, clock.Load()+10, nil, resolve)
	if err != nil || !ready {
		t.Fatalf("brief ready=%t %v", ready, err)
	}
	enqueue("brief", 1, brief)
	newer, ready, err := mem.PrepareBrief(h.ctx, scope, fixture.Topic, 2, 2, clock.Load()+10, nil, resolve)
	if err != nil || !ready {
		t.Fatalf("newer brief ready=%t %v", ready, err)
	}
	enqueue("brief", 2, newer)
	run()
	if calls.Load() != 2 || jobWatermark(t, db, "brief") != 1 {
		t.Fatal("obsolete brief called the model or blocked the queue")
	}
	run()
	if jobWatermark(t, db, "brief") != 2 {
		t.Fatal("latest brief did not progress")
	}
	answer, err := mem.PersonBrief(h.ctx, scope, fixture.Topic, 3, clock.Load()+10, resolve)
	if err != nil || len(answer) != 1 || len(answer[0].Citations) != 1 {
		t.Fatalf("brief: %+v %v", answer, err)
	}
	// Install a synthetic legacy standing instruction to exercise maintenance.
	sourceID := "instruction-source"
	admit(scope.Agent, sourceID, fixture.Instruction, guard.EntryOwner, clock.Load(), "")
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := memory.Insert(h.ctx, tx, memory.Write{Agent: scope.Agent, Person: scope.Person, Item: memory.Item{Kind: memory.Preference, Content: fixture.Instruction, Subject: memory.Subject{Kind: memory.Person, ID: scope.Person}, Basis: memory.Stated, Standing: true, Evidence: []memory.Evidence{{Kind: memory.Event, ID: sourceID, Precision: memory.Exact}}}}, resolve)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	enqueue("maintenance", 1, struct{}{})
	run()
	proposed, err := mem.MaintenanceProposals(h.ctx, scope.Person, memory.All, clock.Load())
	if err != nil || len(proposed) != 1 {
		t.Fatalf("maintenance: %+v %v", proposed, err)
	}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		return memory.SetMaintenanceChoice(h.ctx, tx, memory.MaintenanceChoice{Person: scope.Person, Holder: memory.All, Revision: 1, Allowed: []string{memory.ClearStanding}})
	}); err != nil {
		t.Fatal(err)
	}
	enqueue("maintenance", 2, struct{}{})
	run()
	enqueue("maintenance", 3, struct{}{})
	run()
	if notices.Load() != 2 {
		t.Fatalf("silent pass emitted a notice: %d", notices.Load())
	}
	if calls.Load() != 3 {
		t.Fatalf("maintenance or idle worker called model: %d", calls.Load())
	}
	profile, err := mem.Profile(h.ctx, scope, clock.Load(), 10, 100, func(string) int { return 1 })
	if err != nil || len(profile) != 1 || profile[0].Content != fixture.Statement {
		t.Fatalf("maintained profile: %+v %v", profile, err)
	}
	run()
	if calls.Load() != 3 {
		t.Fatal("finished jobs ran again")
	}
	if err := mem.CheckExtraction(h.ctx, batch, resolve); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := memory.Insert(h.ctx, tx, memory.Write{Agent: scope.Agent, Person: scope.Person, Item: memory.Item{Kind: memory.Fact, Content: "protected burst", Subject: memory.Subject{Kind: memory.Person, ID: scope.Person}, Basis: memory.Stated, Scope: memory.NeverLeaves, Evidence: []memory.Evidence{{Kind: memory.Event, ID: "burst-0", Precision: memory.Exact}}}}, resolve)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CheckExtraction(h.ctx, batch, resolve); err == nil {
		t.Fatal("queued extraction bypassed tightened scope")
	}
	// An obsolete extraction must not hold Watermark+1 forever. Rebuild the
	// replacement from the old source cursor, preserving eligible work.
	extractionID := "extract-" + scope.Agent
	enqueue(extractionID, 2, batch)
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, "UPDATE retract_meta SET epoch=epoch+1")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := mem.PrepareExtraction(h.ctx, scope, batch.After, len(fixture.Burst)+1, 30*time.Second, resolve)
	if err != nil || len(fresh.Utterances) != 1 || len(fresh.Utterances[0].Events) != 1 {
		t.Fatalf("fresh extraction: %+v %v", fresh, err)
	}
	enqueue(extractionID, 3, fresh)
	run()
	if calls.Load() != 3 || jobWatermark(t, db, extractionID) != 2 {
		t.Fatal("obsolete extraction dispatched or blocked its replacement")
	}
	run()
	if calls.Load() != 4 || jobWatermark(t, db, extractionID) != 3 {
		t.Fatal("fresh extraction did not run")
	}
	current, err := mem.Recall(h.ctx, scope, memory.Query{Text: fixture.Statement, At: clock.Load() + 100})
	if err != nil || len(current) != 1 || len(current[0].Evidence) != 3 {
		t.Fatalf("replacement lost eligible evidence: %+v %v", current, err)
	}
}
