// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/backup"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/tools/crw-takeover/importer"
)

// Real operator binary -> canonical records -> installed Core -> model tool
// execution -> durable new result -> backup/restore -> idempotent retry. Core
// reads the restored evidence after the source export has been removed.
func TestM3CRWTakeoverInstalledCore(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "crw-takeover")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", binary, "./tools/crw-takeover")
	build.Dir = "../.."
	build.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "GOENV=off", "GOTOOLCHAIN=auto", "CGO_ENABLED=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "TMPDIR"} {
		if value, ok := os.LookupEnv(name); ok {
			build.Env = append(build.Env, name+"="+value)
		}
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build takeover: %s %v", out, err)
	}
	var imported importer.Report
	var calls atomic.Int32
	result := make(chan coreexec.RecordReceipt, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		output := func(code string, into any) {
			var item struct{ Type, Output string }
			var body struct {
				Code   string
				Output json.RawMessage
			}
			if len(req.Input) == 0 {
				t.Error("empty provider input")
				return
			}
			_ = json.Unmarshal(req.Input[len(req.Input)-1], &item)
			_ = json.Unmarshal([]byte(item.Output), &body)
			if item.Type != "function_call_output" || body.Code != code {
				t.Errorf("tool output: %s", item.Output)
			}
			if err := json.Unmarshal(body.Output, into); err != nil {
				t.Error(err)
			}
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "find-import", "query_records", coreexec.RecordQuery{Search: "workshop report", Kind: "todo", Limit: 10})
		case 2:
			var found struct{ Rows []records.Row }
			output("ok", &found)
			if len(found.Rows) != 1 || found.Rows[0].ID != imported.Rows[2].LINAID {
				t.Errorf("import missing from Core search: %+v", found)
				emitResponse(w, "Missing imported record.", true)
				return
			}
			emitPersonaCall(w, "read-import", "read_record", coreexec.RecordRead{ID: found.Rows[0].ID})
		case 3:
			var got coreexec.RecordReceipt
			output("ok", &got)
			if got.ID != imported.Rows[2].LINAID || !strings.Contains(got.Markdown, "Use measured evidence") || !strings.Contains(got.Markdown, imported.Rows[1].LINAID) {
				t.Errorf("import unreadable in live Core: %+v", got)
			}
			emitPersonaCall(w, "reject-source-id", "read_record", coreexec.RecordRead{ID: "synthetic-issue"})
		case 4:
			var failure struct{ Code string }
			output("failed", &failure)
			if failure.Code != "record_not_found" {
				t.Errorf("source id resolved: %+v", failure)
			}
			emitPersonaCall(w, "save-result", "write_record", coreexec.RecordWrite{Kind: "memo", Title: "Workshop conclusion", Note: "Measured evidence from the imported workshop is preserved."})
		case 5:
			var got coreexec.RecordReceipt
			output("ok", &got)
			result <- got
			emitResponse(w, "The workshop conclusion is saved.", true)
		case 6:
			emitPersonaCall(w, "read-after-restart", "read_record", coreexec.RecordRead{ID: imported.Rows[2].LINAID})
		case 7:
			var got coreexec.RecordReceipt
			output("ok", &got)
			if got.ID != imported.Rows[2].LINAID {
				t.Error("restart lost imported identity")
			}
			emitResponse(w, "The imported evidence remains available.", true)
		default:
			t.Error("unexpected provider request")
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	// This scenario includes installation, import retries, two restores and three
	// Core starts. Compile commands before its runtime budget starts; wait for
	// explicit listening/admission at each start, never for an elapsed delay.
	buildCommand(t, "lina-node")
	h, _ := installedConversationHarness(t, server.URL)
	scenarioContext, stop := context.WithTimeout(t.Context(), 2*time.Minute)
	t.Cleanup(stop)
	h.ctx = scenarioContext
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connectWithin(core, time.Minute).Close()
		waitCoreReady(t, h)
		return core
	}
	core := start()
	client, _ := conversationClient(t, h)
	person, agent := client.Welcome.PersonID, client.Welcome.Agents[0].AgentID
	raw, err := os.ReadFile("../m3/crw-takeover.json")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "snapshot.json")
	if err = os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(wantSuccess bool) importer.Report {
		t.Helper()
		cmd := exec.CommandContext(h.ctx, binary, "--state-root", h.root, "--snapshot", source, "--sha256", fmt.Sprintf("%x", sha256.Sum256(raw)), "--person", person, "--agent", agent)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if !wantSuccess {
			if err == nil || !strings.Contains(stderr.String(), "lock") {
				t.Fatalf("live Core not fenced: %v %s", err, stderr.String())
			}
			return importer.Report{}
		}
		if err != nil {
			t.Fatalf("takeover: %v %s %s", err, stderr.String(), stdout.String())
		}
		var report importer.Report
		if err = json.Unmarshal(stdout.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		if err = report.Validate(4); err != nil {
			t.Fatal(err)
		}
		return report
	}
	run(false)
	client.Close()
	h.stopCore(core)
	capture := func() string {
		t.Helper()
		p := h.command("backup", "--state-root", h.root)
		p.start(t)
		p.wait(t, 0)
		generation := strings.TrimSpace(strings.TrimPrefix(p.stdout.String(), "Verified backup: "))
		if _, err := backup.Verify(h.ctx, generation); err != nil {
			t.Fatal(err)
		}
		return generation
	}
	restore := func(generation string) {
		t.Helper()
		p := h.command("restore", "--state-root", h.root, "--generation", generation)
		p.start(t)
		p.wait(t, 0)
	}
	beforeImport := capture()
	prior := run(true)
	restore(beforeImport)
	if _, err := os.Stat(filepath.Join(h.root, "system/crw-takeover-journal.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pre-import restore retained later reservations", err)
	}
	imported = run(true)
	again := run(true)
	for i, row := range imported.Rows {
		if row.Outcome != "imported" || row.Reused || row.LINAID == prior.Rows[i].LINAID || !again.Rows[i].Reused || row.LINAID != again.Rows[i].LINAID {
			t.Fatal(imported, again)
		}
	}
	core = start()
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "takeover-read", "Read the imported workshop and save a conclusion.")
	var saved coreexec.RecordReceipt
	select {
	case saved = <-result:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if saved.ID == "" || saved.Revision == "" {
		t.Fatal("Core produced no durable result", saved)
	}
	client.Close()
	h.stopCore(core)
	afterImport := capture()
	// A fresh destination has compatible installation controls but no journal
	// or records. The generation must carry both and the result Core produced.
	other := newHarness(t, h.binary)
	other.ctx = scenarioContext
	for _, path := range []string{"system/core/main.json", "system/core/epoch", "system/bin/manifest.json"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(other.root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(h.root, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(other.root, path), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h = other
	restore(afterImport)
	again = run(true)
	for i, row := range imported.Rows {
		if !again.Rows[i].Reused || again.Rows[i].LINAID != row.LINAID {
			t.Fatal("restored import duplicated records", imported, again)
		}
	}
	if err = os.Remove(source); err != nil {
		t.Fatal(err)
	}
	core = start()
	client, conversation = conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "takeover-restart", "Read the imported workshop again.")
	client.Close()
	h.stopCore(core)
	if calls.Load() != 7 {
		t.Fatal("missing real tool execution", calls.Load())
	}
	data, err := os.ReadFile(filepath.Join(h.root, "versions/records/inbox/workshop-conclusion.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != saved.Markdown {
		t.Fatal("result receipt differs from saved bytes")
	}
}
