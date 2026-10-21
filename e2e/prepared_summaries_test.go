// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
	"github.com/thisisjun786/lina/kit/provider"
)

// #34's harness runs the real worker binary against Core's Unix job protocol.
// The model is synthetic; all admission, preparation, Apply and fitting are real.
func TestM1PreparedSummaries(t *testing.T) {
	raw, err := os.ReadFile("m1/prepared-summaries.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		JobID   string `json:"job_id"`
		Fact    string `json:"fact"`
		Padding int    `json:"padding_repetitions"`
		Window  int    `json:"context_window"`
		Keep    int    `json:"keep_user_tokens"`
		Levels  int    `json:"levels"`
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildCommand(t, "moirai-worker"))
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root, "canon.db")
	migrations := canon.Migrations()
	migrations = append(migrations, canon.Migration{Version: migrations[len(migrations)-1].Version + 1, SQL: jobs.Schema + budget.Schema})
	db, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: 1, Controls: controls, Migrations: migrations})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	events := ledger.New(db, nil)
	admit := func(id, body string) compaction.Record {
		t.Helper()
		_, err := events.Admit(h.ctx, guard.EntryOwner, ledger.Input{ID: id, ConversationID: "conversation", PersonID: "person", AgentID: "agent", Surface: ledger.Surface{Kind: "app"}, OccurredAt: time.Unix(1, 0), RecordedAt: time.Unix(1, 0), TimeQuality: "observed", Body: body})
		if err != nil {
			t.Fatal(err)
		}
		item, _ := json.Marshal(map[string]string{"role": "user", "content": body})
		return compaction.Record{ID: id, Item: item, User: true}
	}
	var calls, ids atomic.Int64
	b := &compaction.Builder{Store: compaction.New(db), Events: events, Agent: "agent", Conversation: "conversation", Model: "synthetic-model", PreparationJob: scenario.JobID, Now: time.Now, NextID: func() string { return fmt.Sprintf("summary-%d", ids.Add(1)) }, Generate: func(_ context.Context, segment compaction.PassSegment) (compaction.Text, error) {
		calls.Add(1)
		return compaction.Text{Requests: "none", Topics: "reservation", Actions: "none", Values: "42", Omitted: "wording"}, nil
	}}
	service, err := jobs.New(jobs.Config{Store: db, Epoch: 1, Limit: 100, Definitions: map[string]jobs.Definition{scenario.JobID: {Agent: "agent", Account: "synthetic-account", Provider: provider.ChatGPT, Model: func(ctx context.Context, c jobs.Call) (jobs.Result, error) {
		out, err := b.GeneratePreparation(ctx, c.Input)
		return jobs.Result{Output: out}, err
	}, Apply: b.ApplyPreparation}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Register(h.ctx, jobapi.Job{ID: scenario.JobID, Kind: jobapi.Memory, Enabled: true, Every: time.Minute, Timeout: operationTimeout, Budget: 100, MaxTokens: 10, Adoption: "prepared-summary"}); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(h.root, "run", "jobs.sock")
	startJobService(t, h.ctx, service, socket, controls)
	p := compaction.Pass{Policy: compaction.Policy{ContextWindow: scenario.Window, KeepUserTokens: scenario.Keep}, Count: func(r provider.Request) int { raw, _ := json.Marshal(r); return len(raw) }, Summarize: b.Summarize, Prepared: b.Prepared, Publish: b.Publish}
	v := compaction.View{Request: provider.Request{Model: b.Model, Purpose: provider.Turn}, History: []compaction.Record{admit("fact", scenario.Fact+strings.Repeat("context ", scenario.Padding))}, Input: admit("input", "continue")}
	queued, err := b.QueuePreparation(h.ctx, p, v, func(ctx context.Context, input json.RawMessage) error {
		return service.Enqueue(ctx, scenario.JobID, jobapi.Task{Sequence: 1, Input: input})
	})
	if err != nil || !queued {
		t.Fatalf("queued=%v %v", queued, err)
	}
	worker := h.command("--canon", path, "--socket", socket, "--once")
	worker.start(t)
	worker.wait(t, 0)
	if calls.Load() != 1 || jobWatermark(t, db, scenario.JobID) != 1 {
		t.Fatal("worker did not prepare once")
	}
	f, err := b.Store.Frontier(h.ctx, "conversation")
	if err != nil || f.Root != "" {
		t.Fatalf("preparation published: %+v %v", f, err)
	}
	v.History = append(v.History, v.Input)
	v.Input = admit("next", "continue again")
	fitted, err := p.Fit(h.ctx, v, false)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("foreground called model despite preparation")
	}
	v = fitted.View
	for level := 0; level < scenario.Levels; level++ {
		if level > 0 {
			v.History = append(v.History, admit(fmt.Sprintf("history-%d", level), strings.Repeat("context ", scenario.Padding)))
			fitted, err = p.Fit(h.ctx, v, false)
			if err != nil {
				t.Fatal(err)
			}
			v = fitted.View
		}
		node, err := b.Store.Node(h.ctx, v.RootID)
		if err != nil || node.Level != level {
			t.Fatalf("level=%d node=%+v err=%v", level, node, err)
		}
	}
	expansion := compaction.Expansion{DB: db, Events: events, NodeEvents: b.Store.ExpandEvents}
	pageRaw, err := expansion.Read(h.ctx, "agent", "node:"+v.RootID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var page compaction.ExpansionPage
	if err = json.Unmarshal(pageRaw, &page); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page.Events {
		if e.ID == "fact" {
			found = e.Body == scenario.Fact+strings.Repeat("context ", scenario.Padding)
		}
	}
	if !found {
		t.Fatal("higher tree lost exact raw fact")
	}
	// An idle one-shot worker has no next task and cannot produce rollup nodes.
	before := calls.Load()
	worker = h.command("--canon", path, "--socket", socket, "--once")
	worker.start(t)
	worker.wait(t, 0)
	if calls.Load() != before {
		t.Fatal("idle worker generated a rollup")
	}
}
