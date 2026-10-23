// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/provider"
)

// Seed a retained unknown-effect receipt through the run service while stopped.
// The installed Core performs lane delivery, projection, answering and recovery.
func TestM2EffectQuestionOverviewInstalledCore(t *testing.T) {
	t.Parallel()
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	core := start(true)
	h.stopCore(core)
	id := seedOwnerEffect(t, h)
	core = start(true)
	defer func() { h.stopCore(core) }()
	dial := func() *clientapi.Client {
		t.Helper()
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "effect-overview", []protocol.Capability{{Name: "progress", Version: "1.0"}, {Name: "waiting", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	read := func(c *clientapi.Client, want int) {
		t.Helper()
		mode := "overview"
		if _, err := c.Progress(h.ctx, protocol.ProgressGet{View: &mode}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		p, ok := frame.Payload.(protocol.Progress)
		if err != nil || frame.Error != nil || !ok || p.Waiting == nil || len(*p.Waiting) != want {
			t.Fatalf("overview want=%d: %+v %v", want, frame, err)
		}
		if _, err := c.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
			t.Fatal(err)
		}
		frame, err = c.Receive(h.ctx)
		w, ok := frame.Payload.(protocol.Waiting)
		if err != nil || frame.Error != nil || !ok || w.Rows == nil || len(*w.Rows) != want {
			t.Fatalf("waiting want=%d: %+v %v", want, frame, err)
		}
		if want == 1 && ((*w.Rows)[0].Ref.Source != "run_effect" || (*w.Rows)[0].Ref.Key != id+":effects" || (*w.Rows)[0].Ref.Revision != "1") {
			t.Fatalf("question navigation lost: %+v", w)
		}
	}
	awaitCard := func() {
		t.Helper()
		c := dial()
		defer c.Close()
		topic := "waiting"
		if _, err := c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			frame, err := c.Receive(ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("question delivery: %+v %v", frame, err)
			}
			if snap, ok := frame.Payload.(protocol.Snapshot); ok {
				for _, item := range snap.Items {
					if item.Kind == protocol.KindQuestion {
						var q protocol.Question
						if err = json.Unmarshal(item.Payload, &q); err != nil {
							t.Fatal(err)
						}
						if q.Card != nil && q.Card.ID == id+":effects" {
							return
						}
					}
				}
			}
			if q, ok := frame.Payload.(protocol.Question); ok && q.Card != nil && q.Card.ID == id+":effects" {
				return
			}
		}
	}
	awaitCard()
	c := dial()
	read(c, 1)
	c.Close()
	h.stopCore(core)
	core = start(true)
	awaitCard()
	c = dial()
	defer c.Close()
	read(c, 1)
	observer := dial()
	defer observer.Close()
	topic := "progress"
	if _, err := observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := observer.Receive(h.ctx)
	if _, ok := frame.Payload.(protocol.Snapshot); err != nil || frame.Error != nil || !ok {
		t.Fatalf("subscription: %+v %v", frame, err)
	}
	cardID, revision := id+":effects", "1"
	if _, err = c.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &cardID, Revision: &revision, Answer: protocol.QuestionAnswerActionDecline}); err != nil {
		t.Fatal(err)
	}
	frame, err = c.Receive(h.ctx)
	answer, ok := frame.Payload.(protocol.QuestionAnswer)
	if err != nil || frame.Error != nil || !ok || answer.Outcome == nil || *answer.Outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("answer: %+v %v", frame, err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	for {
		frame, err = observer.Receive(ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("answered progress update: %+v %v", frame, err)
		}
		if p, ok := frame.Payload.(protocol.ProgressUpdate); ok && p.Waiting != nil && len(*p.Waiting) == 0 {
			break
		}
	}
	read(c, 0)
	c.Close()
	observer.Close()
	h.stopCore(core)
	core = start(true)
	c = dial()
	defer c.Close()
	read(c, 0)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err = db.QueryRowContext(h.ctx, "SELECT record FROM background_runs WHERE id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record runs.Record
	if err = json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.State != runs.Landed || record.EffectsQuestion.Answer == nil || record.EffectsQuestion.Answer.Action != "decline" || record.EffectsQuestion.Effects[0].Code != loop.Unknown {
		t.Fatalf("answer changed effect evidence: %+v", record)
	}
}

func seedOwnerEffect(t *testing.T, h *harness) string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	raw, err := ctl.Read("main.json")
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.MintEpoch(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon", "canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	a, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	person := "person:" + main.ID
	first, err := a.FirstRun(h.ctx, person, true)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := assets.Open(h.ctx, h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer materials.Close()
	service, err := runs.New(runs.Config{Epoch: epoch, DB: db, Assets: materials, WorkDir: filepath.Join(h.root, "work"), Authorize: func(context.Context, runs.Origin, runs.Brief) error { return nil }, PublishPromotion: func(context.Context, *canon.Tx, string, budget.Handle, budget.Handoff) error { return nil }, Stream: func(context.Context, provider.Request) (provider.Stream, error) {
		t.Error("fixture run executed")
		return nil, context.Canceled
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	origin := runs.Origin{Person: person, Agent: first.Agent.ID, Conversation: "main:" + first.Agent.ID, Turn: "effect-fixture"}
	brief := runs.Brief{Goal: "Prepare a report", Scope: "read", NotTouch: "effects", Done: "report", Result: "summary", Checks: []runs.Check{{Name: "report", Kind: runs.Claimed}}}
	handle, err := service.StartFromTurn(h.ctx, origin, brief, budget.Handoff{Turn: origin.Turn, Input: "Prepare a report", Effects: []budget.Effect{{CallID: "send", Key: "unknown-send", Tool: "send", Code: loop.Unknown}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Stop(h.ctx, handle.ID); err != nil {
		t.Fatal(err)
	}
	return handle.ID
}
