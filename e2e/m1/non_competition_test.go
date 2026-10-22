// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/kit/protocol"
)

// This is protocol evidence for file safety in a running installed Core. It is
// deliberately not a human concurrent-session measurement of the Linux ladder.
func TestM3NonCompetitionInstalledCore(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../m3/non-competition.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Title, Initial, Outside, Refusal string
		HumanEdit                        string `json:"human_edit"`
		StaleEdit                        string `json:"stale_edit"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte(scenario.Outside), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	created := make(chan coreexec.RecordReceipt, 1)
	// Only the serial fixture provider accesses the receipt.
	var receipt coreexec.RecordReceipt
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if strings.Contains(string(mustJSON(t, request.Input)), scenario.Outside) {
			t.Error("out-of-scope file bytes reached the model")
		}
		result := func(want string, target any) {
			var item struct{ Type, Output string }
			var tool struct {
				Code   string
				Output json.RawMessage
			}
			if len(request.Input) == 0 {
				t.Error("missing tool receipt")
				return
			}
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &tool); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || tool.Code != want {
				t.Errorf("tool outcome = %s, want %s", item.Output, want)
			}
			if target != nil {
				if err := json.Unmarshal(tool.Output, target); err != nil {
					t.Error(err)
				}
			}
		}
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "create-note", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Title, Note: scenario.Initial})
		case 2:
			result("ok", &receipt)
			created <- receipt
			// The model supplies a filesystem path where a record id is required.
			// The real registry/store must refuse it without reading those bytes.
			// Do this while the repository is clean so a dirty-worktree refusal
			// cannot mask an incorrect path lookup.
			emitPersonaCall(w, "outside-read", "read_record", coreexec.RecordRead{ID: outside})
		case 3:
			var failure struct{ Code string }
			result("failed", &failure)
			if failure.Code != "record_not_found" {
				t.Errorf("outside read refusal: %+v", failure)
			}
			emitResponse(w, "Saved the note; the other file was not read.", true)
		case 4:
			emitPersonaCall(w, "stale-write", "write_record", coreexec.RecordWrite{ID: receipt.ID, Revision: receipt.Revision, Kind: "memo", Title: scenario.Title, Note: scenario.StaleEdit})
		case 5:
			result(scenario.Refusal, nil)
			emitResponse(w, "The newer edit was preserved.", true)
		default:
			t.Error("unexpected provider work")
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "create-note", "Save a synthetic note.")
	var saved coreexec.RecordReceipt
	select {
	case saved = <-created:
	case <-h.ctx.Done():
		t.Fatal(h.ctx.Err())
	}
	if saved.ID == "" || saved.Revision == "" || !strings.Contains(saved.Markdown, scenario.Initial) {
		t.Fatalf("no real write receipt: %+v", saved)
	}
	repo := filepath.Join(h.root, "versions", "records")
	paths, err := filepath.Glob(filepath.Join(repo, "inbox", "*.md"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("expected one saved file: %v %v", paths, err)
	}
	before, err := os.ReadFile(paths[0])
	if err != nil || string(before) != saved.Markdown {
		t.Fatalf("receipt does not match saved bytes: %v", err)
	}
	humanBytes := strings.Replace(saved.Markdown, scenario.Initial, scenario.HumanEdit, 1)
	if err := os.WriteFile(paths[0], []byte(humanBytes), 0600); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(repo, ".git", "refs", "heads", "records")
	beforeHead, err := os.ReadFile(head)
	if err != nil {
		t.Fatal(err)
	}
	frames := admittedTurn(t, h, c, conversation, "stale-note", "Try the saved revision and the outside record.")
	completed := false
	for _, frame := range frames {
		if item, ok := frame.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text != "" {
			completed = true
		}
	}
	if !completed || calls.Load() != 5 {
		t.Fatalf("Core did not finish the tool-backed conversation: completed=%t calls=%d", completed, calls.Load())
	}
	c.Close()
	h.stopCore(core)
	for name, expected := range map[string]string{paths[0]: humanBytes, outside: scenario.Outside, head: string(beforeHead)} {
		got, err := os.ReadFile(name)
		if err != nil || string(got) != expected {
			t.Fatalf("refused operation changed file %s: %v", filepath.Base(name), err)
		}
	}
}
