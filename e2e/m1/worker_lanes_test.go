// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type workerLaneScenario struct {
	Command                 string
	Tasks, Workers, Threads []string
	Output                  string
}

// #34's installed binary/PTY harness consumes genuine work ledger records,
// including a content-checked artifact and an interrupted execution. The Core
// process owns projection/recovery and the public socket; no fake Core is used.
func TestM3WorkerLanesInstalledCore(t *testing.T) {
	t.Parallel()

	var scenario workerLaneScenario
	raw, err := os.ReadFile("../m3/worker-lanes.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	core := start(true)
	h.stopCore(core)
	seedWorkerLanes(t, h, scenario)
	core = start(true)
	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 40)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, scenario.Command+"\r")
	readPage := func() protocol.Progress {
		t.Helper()
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatalf("worker lanes refused: %s", e.Error.Code)
			}
			if e.Kind == protocol.KindItemDone {
				t.Fatal("developer command became conversation input")
			}
			if p, ok := e.Payload.(protocol.Progress); ok {
				return p
			}
		}
	}
	page := readPage()
	if page.Lanes == nil || len(*page.Lanes) != 2 {
		t.Fatalf("missing worker sessions: %+v", page)
	}
	first, second := (*page.Lanes)[0], (*page.Lanes)[1]
	if first.Worker != second.Worker || first.LaneID == second.LaneID || first.State != "done" || second.State != "unknown" {
		t.Fatalf("worker identity/outcome lost: %+v", *page.Lanes)
	}
	wait := func(predicate func(string) bool) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return predicate(screenText(s)) })
		if err != nil {
			t.Fatalf("lane screen: %v\n%s", err, screenText(snap))
		}
		return screenText(snap)
	}
	folded := wait(func(s string) bool { return strings.Count(s, "[Codex]") == 2 })
	for _, thread := range scenario.Threads {
		if strings.Contains(folded, thread) {
			t.Fatal("lane unfolded by default")
		}
	}
	h.keys(terminal, "\r")
	wait(func(s string) bool {
		return strings.Contains(s, scenario.Threads[0]) && strings.Contains(s, "worker_report") && strings.Contains(s, "core_review")
	})
	h.keys(terminal, "\x1b[B\r")
	wait(func(s string) bool {
		return strings.Contains(s, scenario.Threads[0]) && strings.Contains(s, scenario.Threads[1])
	})
	h.keys(terminal, "r")
	readPage()
	wait(func(s string) bool {
		return strings.Count(s, "[Codex]") == 2 && strings.Contains(s, scenario.Threads[1])
	})
	h.keys(terminal, "\r")
	wait(func(s string) bool {
		return strings.Contains(s, scenario.Threads[0]) && !strings.Contains(s, scenario.Threads[1])
	})
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, scenario.Command+"\r")
	readPage()
	wait(func(s string) bool { return strings.Count(s, "[Codex]") == 2 && !strings.Contains(s, "thread:") })
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.socket = socket
	h.stopCore(core)
}

func seedWorkerLanes(t *testing.T, h *harness, scenario workerLaneScenario) {
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
	for i, title := range scenario.Tasks {
		id := fmt.Sprintf("lane-task-%d", i)
		dir := filepath.Join(h.root, "work", id)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		task := work.Task{ID: id, PersonID: person, AgentID: first.Agent.ID, ConversationID: "main:" + first.Agent.ID, Title: title, Done: "report content verified", ArtifactRoots: []string{dir}}
		if err = s.Create(h.ctx, task); err != nil {
			t.Fatal(err)
		}
		run := work.Run{ID: "run-" + id, TaskID: id, Generation: 1, Worker: scenario.Workers[i], ThreadID: scenario.Threads[i], TurnID: "turn-" + id, NodeID: "local"}
		if err = s.Start(h.ctx, run); err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			continue
		}
		path := filepath.Join(dir, "report.txt")
		if err = os.WriteFile(path, []byte(scenario.Output), 0600); err != nil {
			t.Fatal(err)
		}
		output, err := os.ReadFile(path)
		if err != nil || string(output) != scenario.Output {
			t.Fatal("artifact verification failed", err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(output))
		now := time.Now().UTC()
		receipt := work.Receipt{SchemaVersion: 1, RelationshipID: id, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now, Manifest: []work.Artifact{{Path: path, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: "report", TaskID: id, NodeID: run.NodeID, Path: path, SHA256: hash, ObservedAt: now}}}
		receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
		receipt.EventID = work.ReceiptID(receipt)
		if err = s.Submit(h.ctx, run.ID, receipt); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(h.ctx, id, run.ID, "completed"); err != nil {
			t.Fatal(err)
		}
		if err = s.Judge(h.ctx, id, receipt.EventID, "verified", "content checked"); err != nil {
			t.Fatal(err)
		}
	}
}
