// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/tools"
)

func TestM2FileLanding(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed=%t", committed), func(t *testing.T) { testM2FileLanding(t, committed) })
	}
}
func testM2FileLanding(t *testing.T, committed bool) {
	raw, err := os.ReadFile("../m2/file-landing.json")
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
		if scriptedWorkCompletion(w, request.Input) {
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
	// Five Core boots plus worker observation, result inspection, interrupted
	// delivery and restart verification share this scenario. Give these phases
	// their own bounded budget instead of the default three-operation budget;
	// each Core connection still has its existing operationTimeout.
	scenarioContext, stopScenario := context.WithTimeout(t.Context(), 10*operationTimeout)
	defer stopScenario()
	h.ctx = scenarioContext
	core := start(true)
	client, conversation := conversationClient(t, h)
	sequence := 0
	invoke := func(name string, args any, ok bool) json.RawMessage {
		t.Helper()
		sequence++
		calls <- call{name, args}
		queuedWorkTurn(t, h, client, conversation, fmt.Sprint("files-", sequence), "Continue the file task.")
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
	raw = invoke("prepare_work", coreexec.PrepareWork{WorkKind: "document", TaskID: scenario.TaskID, Title: scenario.Title, Done: scenario.Done, Destination: destination}, true)
	var workspace work.Workspace
	if err = json.Unmarshal(raw, &workspace); err != nil {
		t.Fatal(err)
	}
	// Keep a real worker alive after it writes the result, so the report tool
	// cannot mistake valid output and a completion claim for observed termination.
	worker := exec.CommandContext(h.ctx, "sh", "-c", `mkdir -p out && printf '%s' "$RESULT" > out/document.txt && printf '%s' "$REPORT" > report.md && printf 'ready\n' && read -r finish`)
	worker.Dir = workspace.Path
	worker.Env = []string{"PATH=" + os.Getenv("PATH"), "RESULT=" + scenario.Output, "REPORT=" + scenario.Report}
	input, err := worker.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := worker.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		input.Close()
		if !waited {
			worker.Process.Kill()
			worker.Wait()
		}
	}()
	ready := make([]byte, len("ready\n"))
	if _, err = io.ReadFull(output, ready); err != nil || string(ready) != "ready\n" {
		t.Fatalf("worker readiness: %q %v", ready, err)
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
	pending := invoke("report_work_result", coreexec.ReportWork{TaskID: scenario.TaskID, RunID: run.ID, Receipt: receipt()}, false)
	var refusal struct {
		Code      string `json:"code"`
		Retryable bool   `json:"retryable"`
		Next      string `json:"next_action"`
	}
	if err = json.Unmarshal(pending, &refusal); err != nil || refusal.Code != "worker_termination_unobserved" || !refusal.Retryable || refusal.Next != "wait_for_worker_termination_then_report_again" {
		t.Fatalf("unobserved report: %s", pending)
	}
	invoke("land_work_result", coreexec.LandWork{TaskID: scenario.TaskID, ReceiptID: receipt().EventID, Fingerprint: "unobserved", Judgment: "Completion claim is not an exit."}, false)
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unobserved worker caused delivery: %v %v", entries, err)
	}
	// The fixture owns the process observation boundary; only successful Wait
	// supplies Finish. The model report path must never supply this observation.
	if _, err = io.WriteString(input, "finish\n"); err != nil {
		t.Fatal(err)
	}
	input.Close()
	err = worker.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	client.Close()
	h.stopCore(core)
	withFileCanon(t, h, func(db *canon.Store) {
		service := work.New(db)
		card, e := service.Get(h.ctx, run.TaskID)
		if e != nil || !card.Active || card.State != "unknown" || card.TurnStatus != "" || card.Receipt != nil || card.FileSnapshot != nil {
			t.Fatalf("report manufactured termination or snapshot: %+v %v", card, e)
		}
		next := run
		next.ID, next.TurnID, next.Generation = "another-worker", "another-turn", 2
		if e = service.Start(h.ctx, next); !errors.Is(e, work.ErrActiveRun) {
			t.Fatalf("report released active worker reservation: %v", e)
		}
		if e = service.Finish(h.ctx, run.TaskID, run.ID, "completed"); e != nil {
			t.Fatal(e)
		}
	})
	core = start(true)
	client, conversation = conversationClient(t, h)
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
	waitCompletionHandled(t, h)
	// The completion turn has its own idle frame. Reconnect after its durable
	// acknowledgement so it cannot finish the next manual tool request.
	client.Close()
	client, conversation = conversationClient(t, h)
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
	entries, _ = os.ReadDir(destination)
	if len(entries) != 0 {
		t.Fatal("changed output copied")
	}
	c = done()
	write(filepath.Join(workspace.Path, "report.md"), scenario.Report+"Correction checked.\n")
	land(false)
	c = done()
	// Directory-only changes keep the file receipt ID. Each new report must
	// still replace the snapshot through the running Core's normal tool path.
	empty := filepath.Join(workspace.Path, "out/empty")
	renamed := filepath.Join(workspace.Path, "out/renamed")
	for _, step := range []struct {
		name   string
		change func() error
	}{
		{"add", func() error { return os.Mkdir(empty, 0700) }},
		{"remove", func() error { return os.Remove(empty) }},
		{"restore", func() error { return os.Mkdir(empty, 0700) }},
		{"rename", func() error { return os.Rename(empty, renamed) }},
	} {
		previous := c
		if err = step.change(); err != nil {
			t.Fatal(err)
		}
		changed = land(false)
		if err = json.Unmarshal(changed, &instruction); err != nil || instruction.Next != "inspect_result_and_report_done_again" {
			t.Fatalf("%s: no fresh-done instruction: %s", step.name, changed)
		}
		c = done()
		if c.Receipt.EventID != previous.Receipt.EventID || c.FileSnapshot.Revision != previous.FileSnapshot.Revision+1 || c.FileSnapshot.Fingerprint == previous.FileSnapshot.Fingerprint || c.Verdict != "" {
			t.Fatalf("%s: directory-only report did not replace snapshot: %+v", step.name, c)
		}
		invoke("land_work_result", coreexec.LandWork{TaskID: c.Task.ID, ReceiptID: previous.Receipt.EventID, Fingerprint: previous.FileSnapshot.Fingerprint, Judgment: "Stale tree."}, false)
	}
	previous := c
	c = done()
	if c.FileSnapshot.Revision != previous.FileSnapshot.Revision || c.FileSnapshot.Fingerprint != previous.FileSnapshot.Fingerprint {
		t.Fatal("unchanged report created another snapshot")
	}
	// Interrupt the real work tool after copying the first material. The registry
	// records an unknown authority effect and keeps its target reservation, just
	// as an unacknowledged call does across process death. Restart the installed
	// Core to exercise both work-ledger and authority recovery before admission.
	client.Close()
	h.stopCore(core)
	partial := filepath.Join(destination, scenario.TaskID)
	withFileCanon(t, h, func(db *canon.Store) {
		interruptFileLanding(t, h, db, c, committed)
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
	if committed {
		if !c.Applied || c.FileEvidence == nil || len(c.FileCopies) != 1 || c.FileCopies[0].Status != "landed" {
			t.Fatalf("lost committed result: %+v", c)
		}
	} else if c.Applied || c.FileEvidence != nil || len(c.FileCopies) != 1 || c.FileCopies[0].Status != "partial" || !strings.Contains(c.FileCopies[0].Problem, "interrupted") {
		t.Fatalf("recovery: %+v", c)
	}
	assertFileAuthorityRecovery(t, h, c.Task.ID)
	wantPath := partial
	wantBundles := 1
	if !committed {
		wantPath += "-v2"
		wantBundles = 2
	}
	var evidence work.FileEvidence
	if err = json.Unmarshal(land(true), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Path != wantPath || evidence.Fingerprint != c.FileSnapshot.Fingerprint || evidence.Tree.Fingerprint() != evidence.Fingerprint {
		t.Fatalf("bad evidence: %+v", evidence)
	}
	for _, f := range evidence.Tree.Files {
		b, e := os.ReadFile(filepath.Join(evidence.Path, f.Path))
		if e != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != f.SHA256 {
			t.Fatal("destination evidence is not a readback")
		}
	}
	if info, err := os.Stat(filepath.Join(evidence.Path, "out/renamed")); err != nil || !info.IsDir() {
		t.Fatalf("renamed empty directory not delivered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(evidence.Path, "out/empty")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old empty directory delivered: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(partial, "out/document.txt"))
	if err != nil || string(b) != scenario.Output+"Correction.\n" {
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
	if c.Completion == nil || c.Completion.State != "claimed" || c.Completion.Label != "not verified" {
		t.Fatalf("file hash readback replaced required document evidence: %+v", c.Completion)
	}
	land(true)
	reader, readErr := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	var breaches int
	readErr = reader.QueryRowContext(h.ctx, "SELECT count(*) FROM done_completions WHERE agent_id=? AND state='claimed'", c.Task.AgentID).Scan(&breaches)
	reader.Close()
	if readErr != nil || breaches != 1 {
		t.Fatalf("restart/retry duplicated task claim: %d %v", breaches, readErr)
	}
	entries, err = os.ReadDir(destination)
	if err != nil || len(entries) != wantBundles {
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

// No snapshot is written in this fixture. It reads the existing retained
// materials, interrupting only the destination copy's second material.
type interruptedFileMaterials struct {
	store     *assets.Store
	interrupt bool
}

func (m interruptedFileMaterials) Prepare(context.Context, io.Reader, string, string) (work.MaterialWrite, error) {
	return nil, errors.New("unexpected snapshot write")
}
func (m interruptedFileMaterials) Open(ctx context.Context, material work.Material) (io.ReadCloser, error) {
	if m.interrupt && material.Path == "report.md" {
		panic("simulated loss during file copy")
	}
	return m.store.OpenRead(ctx, material.ID, material.Revision)
}
func interruptFileLanding(t *testing.T, h *harness, db *canon.Store, c work.Card, committed bool) {
	t.Helper()
	area, err := work.OpenArea(h.root, "")
	if err != nil {
		t.Fatal(err)
	}
	defer area.Close(context.Background())
	materialStore, err := assets.Open(h.ctx, h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer materialStore.Close()
	service := work.NewWithArea(db, area, interruptedFileMaterials{materialStore, !committed})
	revision, err := service.Revision(h.ctx, c.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := canon.PeekEpoch(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var person string
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT person_id FROM assets WHERE id=?", c.FileSnapshot.Materials[0].ID).Scan(&person)
	}); err != nil {
		t.Fatal(err)
	}
	target := "work:" + c.Task.ID
	auth := grant.Request{GrantID: "crash-grant", MainID: "fixture", Epoch: epoch, TaskID: "crash-turn", Host: "fixture", OSUser: "core", Person: person, Agent: c.Task.AgentID, Profile: "work", Revision: revision}
	gate, err := authority.Open(h.ctx, db, authority.Config{Epoch: epoch, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{Identity: auth, Expires: time.Now().Add(time.Minute), Scope: []grant.Permission{{Tool: "land_work_result", Target: target, Write: true}, {Tool: "unrelated_effect", Target: "other", Write: true}}, Revisions: map[string]string{target: revision, "other": revision}}, grant.Environment{Epoch: epoch, Now: time.Now(), OSPermitted: true, Revision: revision}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	var dispatchGate tools.Gate = gate
	if committed {
		dispatchGate = lostFileReceipt{gate}
	}
	registry := tools.NewRegistry(dispatchGate, lifecycle.NewRegistry(nil))
	if _, err = coreexec.RegisterWork(registry, service, c.Task.AgentID, c.Task.ConversationID); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(coreexec.LandWork{TaskID: c.Task.ID, ReceiptID: c.Receipt.EventID, Fingerprint: c.FileSnapshot.Fingerprint, Judgment: "fixture manager checked snapshot"})
	result, err := registry.Dispatch(h.ctx, tools.Call{Key: "crashed-file-landing", Name: "land_work_result", Auth: auth, Args: raw})
	if err == nil || result.State != tools.Unknown {
		t.Fatalf("copy interruption: %+v %v", result, err)
	}
	effect, err := gate.Effect(h.ctx, "crashed-file-landing")
	wantState := tools.Unknown
	if committed {
		wantState = tools.None
	}
	if err != nil || effect.State != wantState {
		t.Fatalf("missing unfinished effect: %+v %v", effect, err)
	}
	var active string
	if err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT active_key FROM authority_targets WHERE target=?", target).Scan(&active)
	}); err != nil || active != effect.Key {
		t.Fatalf("missing reservation: %q %v", active, err)
	}
	card, err := service.Get(h.ctx, c.Task.ID)
	wantCopy := "copying"
	if committed {
		wantCopy = "landed"
	}
	if err != nil || card.Applied != committed || len(card.FileCopies) != 1 || card.FileCopies[0].Status != wantCopy {
		t.Fatalf("wrong interruption boundary: %+v %v", card, err)
	}
	// An unrelated unknown effect must retain its reservation at restart.
	extra := tools.Call{Key: "unrelated-call", Name: "unrelated_effect", Auth: auth, Args: json.RawMessage(`{}`)}
	if _, err = gate.Begin(h.ctx, extra, tools.Spec{Name: extra.Name}, tools.Input{Arguments: extra.Args, Target: "other", Write: true}); err != nil {
		t.Fatal(err)
	}
}

// This gate loses only the final acknowledgement. The real handler has already
// copied, read back and committed the file evidence; authority still owns a
// running reservation, the second crash window in the same lifecycle.
type lostFileReceipt struct{ *authority.Service }

func (g lostFileReceipt) Finish(context.Context, tools.Ticket, tools.Result) error {
	return errors.New("simulated loss before authority receipt")
}
func assertFileAuthorityRecovery(t *testing.T, h *harness, task string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		for _, expected := range []struct {
			key, target, active string
			state               tools.State
		}{
			{"crashed-file-landing", "work:" + task, "", tools.Cancelled},
			{"unrelated-call", "other", "unrelated-call", tools.Unknown},
		} {
			var raw []byte
			var active string
			if err := tx.QueryRowContext(h.ctx, "SELECT record FROM authority_effects WHERE effect_key=?", expected.key).Scan(&raw); err != nil {
				return err
			}
			var effect authority.Effect
			if err := json.Unmarshal(raw, &effect); err != nil {
				return err
			}
			if err := tx.QueryRowContext(h.ctx, "SELECT active_key FROM authority_targets WHERE target=?", expected.target).Scan(&active); err != nil {
				return err
			}
			if effect.State != expected.state || active != expected.active {
				return fmt.Errorf("recovery %s: state %s active %q", expected.key, effect.State, active)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
