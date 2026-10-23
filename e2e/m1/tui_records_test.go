// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2TUIRecordsInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("../m2/tui-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Title, Note, Capture string }
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	var saved coreexec.RecordReceipt
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			emitPersonaCall(w, "save-memo", "write_record", coreexec.RecordWrite{Kind: "memo", Title: scenario.Title, Note: scenario.Note})
		case 2:
			var request struct{ Input []json.RawMessage }
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			var item struct{ Output string }
			var result struct {
				Code   string
				Output coreexec.RecordReceipt
			}
			if len(request.Input) == 0 {
				t.Error("missing receipt")
				return
			}
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			_ = json.Unmarshal([]byte(item.Output), &result)
			if result.Code != "ok" || result.Output.ID == "" || result.Output.Revision == "" {
				t.Errorf("memo was not saved: %s", item.Output)
			}
			saved = result.Output
			emitResponse(w, "MEMO-SAVED", true)
		default:
			t.Error("record navigation reached the conversation provider")
			emitResponse(w, "UNEXPECTED", true)
		}
	}))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	terminal, tap := h.startConversationTUI(100, 34, "TERM=xterm-256color", "LC_ALL=C")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "Save the picnic memo.\r")
	waitScreen(t, h, terminal, "MEMO-SAVED")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
	core = start(true)
	socket := h.socket
	h.socket = tap.socket
	terminal = h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, 100, 34)
	h.socket = socket
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/help\r")
	waitScreen(t, h, terminal, design.Text(design.English, "shortcuts.title"))
	h.keys(terminal, "\x1b[F")
	screen := waitScreen(t, h, terminal, "/web")
	if !strings.Contains(screenText(screen), "/records") || !strings.Contains(screenText(screen), "/add") {
		t.Fatalf("record inbox and quick add are not discoverable: %s", screenText(screen))
	}
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/memos 연필\r")
	var queried protocol.RecordResult
	for {
		e := tap.next(t, h)
		if e.Kind == protocol.KindRecordResult {
			queried = e.Payload.(protocol.RecordResult)
			break
		}
		if e.Error != nil {
			t.Fatal(e.Error)
		}
	}
	if len(queried.Rows) != 1 || queried.Rows[0].Ref != saved.ID || queried.Rows[0].Revision != saved.Revision {
		t.Fatalf("restart/query lost memo: %+v", queried)
	}
	screen = waitScreen(t, h, terminal, scenario.Title)
	if strings.Contains(screenText(screen), saved.ID) || strings.Contains(screenText(screen), saved.Revision) {
		t.Fatal("list exposed identity")
	}
	h.keys(terminal, "\r")
	screen = waitScreen(t, h, terminal, scenario.Note)
	if strings.Contains(screenText(screen), saved.ID) || strings.Contains(screenText(screen), saved.Revision) {
		t.Fatal("detail exposed identity")
	}
	h.keys(terminal, "\x1b")
	waitScreen(t, h, terminal, design.Text(design.English, "records.keys"))
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	if calls.Load() != 2 {
		t.Fatal("direct record browser called model")
	}

	// Observe canon through an independent client while the real TUI previews.
	observer, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "records", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	todos := func() []protocol.RecordRow {
		t.Helper()
		view := "list"
		id, err := observer.Record(h.ctx, protocol.RecordRequest{Operation: "query", View: &view})
		if err != nil {
			t.Fatal(err)
		}
		for {
			e, err := observer.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo != nil && *e.ReplyTo == id {
				if e.Error != nil {
					t.Fatal(e.Error)
				}
				result := e.Payload.(protocol.RecordResult)
				if result.Code != nil {
					t.Fatal(*result.Code)
				}
				return result.Rows
			}
		}
	}
	nextRecord := func(operation string) protocol.RecordResult {
		t.Helper()
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatal(e.Error)
			}
			if result, ok := e.Payload.(protocol.RecordResult); ok && result.Operation == operation {
				if result.Code != nil {
					t.Fatal(*result.Code)
				}
				return result
			}
		}
	}
	h.keys(terminal, "/add "+scenario.Capture+" 2026-10-25\r")
	preview := nextRecord("preview")
	if preview.Token == nil || len(preview.Rows) != 1 {
		t.Fatal("no preview", preview)
	}
	waitPreview := func(date string) {
		t.Helper()
		if len(preview.Rows) != 1 {
			t.Fatal("missing preview row", preview)
		}
		// The input line already contains the date before the preview arrives.
		// Observe the actionable preview in a complete frame before pressing keys.
		screen, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
			cells := screenText(s)
			return renderedFrame(terminal, s) && strings.Contains(cells, preview.Rows[0].Title) &&
				strings.Contains(cells, design.Text(design.English, "records.confirm")) && strings.Contains(cells, date)
		})
		if err != nil {
			t.Fatalf("record preview not ready: %v; cells=%s", err, screenText(screen))
		}
	}
	waitPreview("2026-10-25")
	if len(todos()) != 0 {
		t.Fatal("preview saved before confirmation")
	}
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	if len(todos()) != 0 {
		t.Fatal("cancel saved capture")
	}
	h.keys(terminal, "/add "+scenario.Capture+"\r")
	preview = nextRecord("preview")
	waitPreview("")
	if len(todos()) != 0 {
		t.Fatal("second preview saved early")
	}
	h.keys(terminal, "\r")
	committed := nextRecord("save")
	waitScreen(t, h, terminal, design.Text(design.English, "records.saved"))
	if len(committed.Rows) != 1 || len(todos()) != 1 {
		t.Fatal("confirmation did not save exactly one record")
	}
	h.keys(terminal, "\x1b")
	waitComposerReady(t, h, terminal)
	h.keys(terminal, "/records anytime\r")
	listed := nextRecord("query")
	if len(listed.Rows) != 1 || listed.Rows[0].Ref != committed.Rows[0].Ref {
		t.Fatal("anytime view lost undated capture", listed)
	}
	waitScreen(t, h, terminal, committed.Rows[0].Title)
	if calls.Load() != 2 {
		t.Fatal("explicit capture or browser called model")
	}

	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	h.stopCore(core)
}
