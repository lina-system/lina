// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

type progressSeenScenario struct {
	Title, Output string
	Actions       []string
}

// TestM2ProgressSeenReviewInstalledCore (m2/progress-seen-review.json) checks
// the installed Core's progress and running views with work-service records and
// content-checked fixture artifacts. Opening or dismissing a ready-for-review
// result removes its progress line and publishes the change. Restart preserves
// the acknowledgement without changing the work card, marking it applied or
// deleting the artifact.
//
// Real work-ledger records enter through the service while Core is stopped.
// The installed Core owns recovery, acknowledgement and live publication.
func TestM2ProgressSeenReviewInstalledCore(t *testing.T) {
	t.Parallel()
	var scenario progressSeenScenario
	raw, err := os.ReadFile("../m2/progress-seen-review.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	core := start(true)
	h.stopCore(core)
	cards := seedProgressReview(t, h, scenario)
	core = start(true)
	dial := func() *clientapi.Client {
		t.Helper()
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "progress-seen", []protocol.Capability{{Name: "progress", Version: "1.0"}, {Name: "running", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c := dial()
	defer c.Close()
	read := func(want int) {
		t.Helper()
		mode := "overview"
		if _, err := c.Progress(h.ctx, protocol.ProgressGet{View: &mode}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		p, ok := frame.Payload.(protocol.Progress)
		if err != nil || frame.Error != nil || !ok || p.Rows == nil {
			t.Fatalf("progress unavailable: %+v %v", frame, err)
		}
		if len(*p.Rows) != want {
			t.Fatalf("progress wants %d rows, got %+v", want, *p.Rows)
		}
		for _, row := range *p.Rows {
			if row.Line.Stage != "unknown" || row.Line.Percent != nil || row.Line.JustFinished != "unknown" {
				t.Fatalf("reviewable work became completed plan: %+v", row)
			}
		}
	}
	read(len(cards))
	observer := dial()
	defer observer.Close()
	topic := "progress"
	if _, err = observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := observer.Receive(h.ctx)
	if _, ok := frame.Payload.(protocol.Snapshot); err != nil || frame.Error != nil || !ok {
		t.Fatalf("progress subscription: %+v %v", frame, err)
	}
	runningCount := func(want int) {
		t.Helper()
		reader := dial()
		defer reader.Close()
		topic := "running"
		if _, err := reader.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err := reader.Receive(h.ctx)
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if err != nil || frame.Error != nil || !ok || len(snapshot.Items) != want {
			t.Fatalf("running wants %d rows: %+v %v", want, frame, err)
		}
	}
	runningCount(len(cards))
	for i, action := range scenario.Actions {
		id := "task:" + cards[i].Task.ID + ":1"
		if _, err = c.Running(h.ctx, protocol.RunningGet{RunID: &id, Action: &action}); err != nil {
			t.Fatal(err)
		}
		frame, err = c.Receive(h.ctx)
		result, ok := frame.Payload.(protocol.Running)
		if err != nil || frame.Error != nil || !ok {
			t.Fatalf("%s failed: %+v %v", action, frame, err)
		}
		if action == "open" && (result.Text == nil || !strings.Contains(*result.Text, "ready_for_review")) {
			t.Fatalf("reviewable result unavailable: %+v", result)
		}
		want := len(cards) - i - 1
		runningCount(want)
		read(want)
		// Await the actual invalidation, never a delay followed by a lucky read.
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		for {
			frame, err = observer.Receive(ctx)
			if err != nil || frame.Error != nil {
				cancel()
				t.Fatalf("%s progress update: %+v %v", action, frame, err)
			}
			if p, ok := frame.Payload.(protocol.ProgressUpdate); ok && p.Rows != nil && len(*p.Rows) == want {
				break
			}
		}
		cancel()
	}
	c.Close()
	observer.Close()
	h.stopCore(core)
	core = start(true)
	c = dial()
	defer c.Close()
	read(0)
	runningCount(0)
	reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	for _, before := range cards {
		after, err := work.New(reader).Get(h.ctx, before.Task.ID)
		if err != nil || !reflect.DeepEqual(before, after) || after.State != "ready_for_review" || after.Applied {
			t.Fatalf("acknowledgement changed work verdict or result: %+v %v", after, err)
		}
		output, err := os.ReadFile(after.Receipt.Manifest[0].Path)
		if err != nil || string(output) != scenario.Output {
			t.Fatalf("acknowledgement removed result: %q %v", output, err)
		}
	}
	h.stopCore(core)
}

func seedProgressReview(t *testing.T, h *harness, scenario progressSeenScenario) []work.Card {
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
	s := work.New(db)
	cards := []work.Card{}
	for _, action := range scenario.Actions {
		dir := filepath.Join(h.root, "work", action)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		task := work.Task{ID: "review-" + action, PersonID: person, AgentID: first.Agent.ID, ConversationID: "main:" + first.Agent.ID, Title: scenario.Title, Done: "artifact applied", ArtifactRoots: []string{dir}, RequiresApply: true}
		if err = s.Create(h.ctx, task); err != nil {
			t.Fatal(err)
		}
		run := work.Run{ID: "run-" + action, TaskID: task.ID, Generation: 1, Worker: "fixture", ThreadID: "thread-" + action, TurnID: "turn-" + action, NodeID: "local"}
		if err = s.Start(h.ctx, run); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "report.txt")
		if err = os.WriteFile(path, []byte(scenario.Output), 0600); err != nil {
			t.Fatal(err)
		}
		output, err := os.ReadFile(path)
		if err != nil || string(output) != scenario.Output {
			t.Fatal("artifact content check failed", err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(output))
		now := time.Now().UTC()
		receipt := work.Receipt{SchemaVersion: 1, RelationshipID: task.ID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: path, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "report", TaskID: task.ID, NodeID: run.NodeID, Path: path, SHA256: hash, ObservedAt: now}}}
		receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
		receipt.EventID = work.ReceiptID(receipt)
		if err = s.Submit(h.ctx, run.ID, receipt); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(h.ctx, task.ID, run.ID, "completed"); err != nil {
			t.Fatal(err)
		}
		if err = s.Judge(h.ctx, task.ID, receipt.EventID, "verified", "artifact content checked; application pending"); err != nil {
			t.Fatal(err)
		}
		card, err := s.Get(h.ctx, task.ID)
		if err != nil || card.State != "ready_for_review" || card.Applied {
			t.Fatalf("invalid review fixture: %+v %v", card, err)
		}
		cards = append(cards, card)
	}
	return cards
}
