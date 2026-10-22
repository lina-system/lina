// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestM2FileLanding(t *testing.T) {
	raw, err := os.ReadFile("m2/file-landing.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		TaskID                      string `json:"task_id"`
		Title, Done, Output, Report string
		Tools                       []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	type call struct {
		name string
		args any
	}
	calls := make(chan call, 1)
	results := make(chan struct {
		Code   string
		Output json.RawMessage
	}, 1)
	awaiting := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input, Tools []json.RawMessage }
		if e := json.NewDecoder(r.Body).Decode(&request); e != nil {
			t.Error(e)
			return
		}
		if awaiting {
			var item struct{ Output string }
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			var result struct {
				Code   string
				Output json.RawMessage
			}
			_ = json.Unmarshal([]byte(item.Output), &result)
			results <- result
			awaiting = false
			emitResponse(w, "Recorded.", true)
			return
		}
		names := map[string]bool{}
		for _, raw := range request.Tools {
			var d struct{ Name string }
			_ = json.Unmarshal(raw, &d)
			names[d.Name] = true
		}
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("running Core missing %s", name)
			}
		}
		select {
		case next := <-calls:
			awaiting = true
			emitPersonaCall(w, "fixture-call", next.name, next.args)
		default:
			t.Error("unexpected provider turn")
			emitResponse(w, "Unexpected.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	client, conversation := conversationClient(t, h)
	sequence := 0
	invoke := func(name string, args any, ok bool) json.RawMessage {
		t.Helper()
		sequence++
		calls <- call{name, args}
		admittedTurn(t, h, client, conversation, fmt.Sprint("files-", sequence), "Continue the file task.")
		select {
		case result := <-results:
			if (result.Code == "ok") != ok {
				t.Fatalf("%s: %s %s", name, result.Code, result.Output)
			}
			return result.Output
		default:
			t.Fatal("no tool result")
			return nil
		}
	}
	destination := t.TempDir()
	raw = invoke("prepare_work", coreexec.PrepareWork{TaskID: scenario.TaskID, Title: scenario.Title, Done: scenario.Done, Destination: destination}, true)
	var workspace work.Workspace
	if err = json.Unmarshal(raw, &workspace); err != nil {
		t.Fatal(err)
	}
	// The deterministic worker is a subprocess; its exit is observed before the
	// completion tool reports done. No vendor worker or external plugin is used.
	worker := exec.Command("sh", "-c", `mkdir -p out && printf '%s' "$RESULT" > out/document.txt && printf '%s' "$REPORT" > report.md`)
	worker.Dir = workspace.Path
	worker.Env = []string{"PATH=" + os.Getenv("PATH"), "RESULT=" + scenario.Output, "REPORT=" + scenario.Report}
	if b, e := worker.CombinedOutput(); e != nil {
		t.Fatalf("worker: %v %s", e, b)
	}
	run := work.Run{ID: "file-worker", TaskID: scenario.TaskID, Generation: 1, Worker: "stub", ThreadID: "file-thread", TurnID: "file-turn", NodeID: "fixture"}
	client.Close()
	h.stopCore(core)
	withFileCanon(t, h, func(db *canon.Store) {
		if e := work.New(db).Start(h.ctx, run); e != nil {
			t.Fatal(e)
		}
	})
	core = start(true)
	client, conversation = conversationClient(t, h)
	receipt := func() work.Receipt {
		t.Helper()
		now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
		r := work.Receipt{SchemaVersion: 1, RelationshipID: scenario.TaskID, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: run.ThreadID, TurnID: run.TurnID, TurnStatus: "completed"}, EmittedAt: now}
		for _, p := range []string{"out/document.txt", "report.md"} {
			path := filepath.Join(workspace.Path, p)
			b, e := os.ReadFile(path)
			if e != nil {
				t.Fatal(e)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(b))
			r.Manifest = append(r.Manifest, work.Artifact{Path: path, SHA256: hash})
			r.Provenance = append(r.Provenance, work.Provenance{ArtifactID: p, TaskID: scenario.TaskID, NodeID: run.NodeID, Path: path, SHA256: hash, ObservedAt: now})
		}
		r.RevisionHash = work.ManifestHash(r.Manifest)
		r.EventID = work.ReceiptID(r)
		return r
	}
	done := func() work.Card {
		t.Helper()
		raw := invoke("report_work_result", coreexec.ReportWork{TaskID: scenario.TaskID, RunID: run.ID, Receipt: receipt()}, true)
		var c work.Card
		if e := json.Unmarshal(raw, &c); e != nil {
			t.Fatal(e)
		}
		if c.FileSnapshot == nil || len(c.FileSnapshot.Materials) != 2 || c.Applied || c.Active {
			t.Fatalf("invalid done: %+v", c)
		}
		return c
	}
	c := done()
	land := func(ok bool) json.RawMessage {
		return invoke("land_work_result", coreexec.LandWork{TaskID: c.Task.ID, ReceiptID: c.Receipt.EventID, Fingerprint: c.FileSnapshot.Fingerprint, Judgment: "Read the document and report against the fixture question."}, ok)
	}
	write := func(path, body string) {
		t.Helper()
		if e := os.WriteFile(path, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(filepath.Join(workspace.Path, "out/document.txt"), scenario.Output+"Correction.\n")
	changed := land(false)
	var instruction struct {
		Next string `json:"next_action"`
	}
	if err = json.Unmarshal(changed, &instruction); err != nil || instruction.Next != "inspect_result_and_report_done_again" {
		t.Fatalf("no fresh-done instruction: %s", changed)
	}
	entries, _ := os.ReadDir(destination)
	if len(entries) != 0 {
		t.Fatal("changed output copied")
	}
	c = done()
	write(filepath.Join(workspace.Path, "report.md"), scenario.Report+"Correction checked.\n")
	land(false)
	c = done()
	// Install the exact durable prefix of an interrupted copy, while Core is
	// stopped. Unit tests interrupt the real copy at the same material boundary;
	// this fixture makes process recovery deterministic without timing a kill.
	client.Close()
	h.stopCore(core)
	partial := filepath.Join(destination, scenario.TaskID)
	if err = os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(partial, "partial.txt"), "keep this interrupted copy")
	withFileCanon(t, h, func(db *canon.Store) {
		s := work.New(db)
		if e := s.Judge(h.ctx, c.Task.ID, c.Receipt.EventID, "verified", "fixture manager checked snapshot"); e != nil {
			t.Fatal(e)
		}
		event := work.Event{Key: "fixture-interrupted-copy", TaskID: c.Task.ID, Kind: "copy_started", FileCopy: &work.FileCopy{Path: partial, Fingerprint: c.FileSnapshot.Fingerprint, Status: "copying"}}
		b, _ := json.Marshal(event)
		if e := db.Write(h.ctx, func(tx *canon.Tx) error {
			_, e := tx.ExecContext(h.ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", c.Task.ID, event.Key, b)
			return e
		}); e != nil {
			t.Fatal(e)
		}
	})
	core = start(true)
	client, conversation = conversationClient(t, h)
	readCard := func() work.Card {
		t.Helper()
		db, e := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if e != nil {
			t.Fatal(e)
		}
		defer db.Close(context.Background())
		c, e := work.New(db).Get(h.ctx, scenario.TaskID)
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	c = readCard()
	if c.Applied || c.FileEvidence != nil || len(c.FileCopies) != 1 || c.FileCopies[0].Status != "partial" || !strings.Contains(c.FileCopies[0].Problem, "interrupted") {
		t.Fatalf("recovery: %+v", c)
	}
	var evidence work.FileEvidence
	if err = json.Unmarshal(land(true), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Path != filepath.Join(destination, scenario.TaskID+"-v2") || evidence.Fingerprint != c.FileSnapshot.Fingerprint || evidence.Tree.Fingerprint() != evidence.Fingerprint {
		t.Fatalf("bad evidence: %+v", evidence)
	}
	for _, f := range evidence.Tree.Files {
		b, e := os.ReadFile(filepath.Join(evidence.Path, f.Path))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != f.SHA256 {
			t.Fatal("destination evidence is not a readback")
		}
	}
	b, err := os.ReadFile(filepath.Join(partial, "partial.txt"))
	if err != nil || string(b) != "keep this interrupted copy" {
		t.Fatal("partial copy overwritten or removed")
	}
	client.Close()
	h.stopCore(core)
	core = start(true)
	client, conversation = conversationClient(t, h)
	c = readCard()
	if !c.Applied || c.State != "done" || c.FileEvidence == nil {
		t.Fatalf("lost evidence: %+v", c)
	}
	land(true)
	entries, err = os.ReadDir(destination)
	if err != nil || len(entries) != 2 {
		t.Fatal("restart duplicated bundle")
	}
	client.Close()
	h.stopCore(core)
}

func withFileCanon(t *testing.T, h *harness, fn func(*canon.Store)) {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.root, "canon/canon.sqlite")
	_, epoch, err := canon.Inspect(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	fn(db)
}
