// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	coreexec "github.com/thisisjun786/lina/internal/core/exec"
)

func TestM2RecordsInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("m2/records.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Title, Note, Updated string
		Tools                []string
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var first, second coreexec.RecordReceipt // Serial provider loop owns these.
	receipts := make(chan coreexec.RecordReceipt, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct{ Name string }
			Input []json.RawMessage
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		n := calls.Add(1)
		names := map[string]bool{}
		for _, tool := range request.Tools {
			names[tool.Name] = true
		}
		for _, name := range scenario.Tools {
			if !names[name] {
				t.Errorf("installed Core does not offer %s", name)
			}
		}
		result := func(want string, out any) {
			var item struct{ Type, Output string }
			var result struct {
				Code   string
				Output json.RawMessage
			}
			if len(request.Input) == 0 {
				t.Error("empty provider input")
				return
			}
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &item); err != nil {
				t.Error(err)
			}
			if err := json.Unmarshal([]byte(item.Output), &result); err != nil {
				t.Error(err)
			}
			if item.Type != "function_call_output" || result.Code != want {
				t.Errorf("request %d: %s", n, item.Output)
			}
			if err := json.Unmarshal(result.Output, out); err != nil {
				t.Error(err)
			}
		}
		switch n {
		case 1:
			emitPersonaCall(w, "create", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Title, Note: scenario.Note})
		case 2:
			result("ok", &first)
			if first.ID == "" || len(first.Revision) != 40 || !strings.Contains(first.Markdown, scenario.Note) {
				t.Errorf("creation receipt: %+v", first)
			}
			receipts <- first
			emitResponse(w, "Saved.", true)
		case 3:
			emitPersonaCall(w, "read-after-restart", "read_record", coreexec.RecordRead{ID: first.ID})
		case 4:
			var got coreexec.RecordReceipt
			result("ok", &got)
			if got != first {
				t.Error("restart lost record", got)
			}
			emitPersonaCall(w, "update", "write_record", coreexec.RecordWrite{ID: first.ID, Revision: first.Revision, Kind: "memo", Title: scenario.Title, Note: scenario.Updated})
		case 5:
			result("ok", &second)
			if second.ID != first.ID || second.Revision == first.Revision || !strings.Contains(second.Markdown, scenario.Updated) {
				t.Errorf("update receipt: %+v", second)
			}
			emitPersonaCall(w, "stale", "write_record", coreexec.RecordWrite{ID: first.ID, Revision: first.Revision, Kind: "memo", Title: scenario.Title, Note: "This stale write must not appear."})
		case 6:
			var failure struct{ Code string }
			result("denied", &failure)
			if failure.Code != "stale_revision" {
				t.Errorf("stale refusal: %+v", failure)
			}
			emitPersonaCall(w, "read-current", "read_record", coreexec.RecordRead{ID: first.ID})
		case 7:
			var got coreexec.RecordReceipt
			result("ok", &got)
			if got != second {
				t.Error("stale write changed current record", got)
			}
			receipts <- got
			emitResponse(w, "Updated.", true)
		default:
			t.Errorf("unexpected request %d", n)
			emitResponse(w, "Unexpected request.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "records-create", "Save these workshop notes.")
	created := <-receipts
	c.Close()
	h.stopCore(core)
	core = start(true)
	c, conversation = conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "records-revise", "Read and update the saved workshop notes.")
	updated := <-receipts
	c.Close()
	h.stopCore(core)
	if calls.Load() != 7 {
		t.Fatalf("provider calls=%d, want 7", calls.Load())
	}
	repo := filepath.Join(h.root, "versions/records")
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1"}
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, raw, err)
		}
		return strings.TrimSpace(string(raw))
	}
	if count := runGit("rev-list", "--count", "HEAD"); count != "2" {
		t.Fatal("writes did not produce exactly two commits", count)
	}
	if head := runGit("rev-parse", "HEAD"); head != updated.Revision {
		t.Fatal("receipt before Git commit", head, updated.Revision)
	}
	if status := runGit("status", "--porcelain"); status != "" {
		t.Fatal("unclean repository", status)
	}
	runGit("fsck", "--strict")
	name := runGit("ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(name, created.ID) {
		t.Fatal("id leaked into record path", name)
	}
	if saved := runGit("show", "HEAD:"+name); saved != strings.TrimSpace(updated.Markdown) {
		t.Fatal("saved bytes differ from tool receipt")
	}
	if original := runGit("show", created.Revision+":"+name); original != strings.TrimSpace(created.Markdown) {
		t.Fatal("old version was not preserved")
	}
}
