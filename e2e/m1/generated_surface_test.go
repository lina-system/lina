// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/result"
)

func TestGeneratedSurfaceInstalledCore(t *testing.T) {
	raw, err := os.ReadFile("../../kit/result/testdata/choice.json")
	if err != nil {
		t.Fatal(err)
	}
	var card result.Surface
	if err := json.Unmarshal(raw, &card); err != nil {
		t.Fatal(err)
	}
	// This test drives the research choice card through a real installed Core,
	// its model tool loop, the Unix protocol, a real TUI PTY, and a restart.
	batch := result.SurfaceBatch{Messages: []json.RawMessage{
		json.RawMessage(`{"version":"v0.9","createSurface":{"surfaceId":"dentist","catalogId":"lina.cards"}}`),
		mustJSON(t, map[string]any{"version": "v0.9", "updateComponents": map[string]any{"surfaceId": "dentist", "components": card.Components}}),
		mustJSON(t, map[string]any{"version": "v0.9", "updateDataModel": map[string]any{"surfaceId": "dentist", "value": card.Data}}),
	}}
	present := result.PresentRequest{Type: result.SurfaceType, Title: "Dentist appointment", Summary: "Choose a time.", Body: mustJSON(t, batch)}
	var calls atomic.Int32
	var resultID string // Accessed only by sequential provider requests.
	var record coreexec.RecordReceipt
	records := make(chan coreexec.RecordReceipt, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		output := func(want string, out any) {
			var item struct{ Type, Output string }
			var reply struct {
				Code   string
				Output json.RawMessage
			}
			if len(request.Input) == 0 {
				t.Error("missing tool reply")
				return
			}
			_ = json.Unmarshal(request.Input[len(request.Input)-1], &item)
			_ = json.Unmarshal([]byte(item.Output), &reply)
			if reply.Code != want {
				t.Errorf("tool code=%s, want %s: %s", reply.Code, want, item.Output)
			}
			if err := json.Unmarshal(reply.Output, out); err != nil {
				t.Error(err)
			}
		}
		switch calls.Add(1) {
		case 1:
			bad := present
			bad.Body = json.RawMessage(strings.Replace(string(present.Body), `"ChoicePicker"`, `"Image"`, 1))
			emitPersonaCall(w, "invalid-card", "present_surface", bad)
		case 2:
			var failure struct {
				Version string
				Error   struct{ Code, Path, Message, SurfaceID string }
			}
			output("failed", &failure)
			if failure.Version != "v0.9" || failure.Error.Code != "VALIDATION_FAILED" || failure.Error.Path != "/" {
				t.Errorf("missing private A2UI error: %+v", failure)
			}
			emitPersonaCall(w, "choice-card", "present_surface", present)
		case 3:
			var published result.Card
			output("ok", &published)
			resultID = published.ID
			if resultID == "" || published.Revision != 1 {
				t.Errorf("invalid published card: %+v", published)
			}
			emitResponse(w, "Choose a time.", true)
		case 4:
			input := string(mustJSON(t, request.Input))
			if !strings.Contains(input, "wed-1500") || !strings.Contains(input, "choose") {
				t.Error("idle card press did not reach the next model turn")
			}
			emitPersonaCall(w, "save-choice", "write_record", coreexec.RecordWrite{Kind: "memo", Title: "Appointment choice", Note: "Wednesday at 15:00"})
		case 5:
			output("ok", &record)
			if record.ID == "" || record.Revision == "" || !strings.Contains(record.Markdown, "Wednesday at 15:00") {
				t.Errorf("normal tool authority did not save result: %+v", record)
			}
			records <- record
			update := present
			update.ID = resultID
			update.BaseRevision = 1
			update.Body = json.RawMessage(`{"messages":[{"version":"v0.9","updateDataModel":{"surfaceId":"dentist","path":"/slot","value":["wed-1500"]}}]}`)
			emitPersonaCall(w, "selected-card", "present_surface", update)
		case 6:
			var published result.Card
			output("ok", &published)
			if published.ID != resultID || published.Revision != 2 {
				t.Errorf("not one revision: %+v", published)
			}
			emitResponse(w, "Saved your choice.", true)
		default:
			t.Error("unexpected model turn")
			emitResponse(w, "Unexpected.", true)
		}
	}))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	core := start(true)
	defer func() {
		if core != nil {
			h.stopCore(core)
		}
	}()
	c, conversation := conversationClient(t, h, protocol.SurfaceResultsCapability())
	if c.Welcome.ProtocolVersion != "1.1" {
		t.Fatalf("version=%s", c.Welcome.ProtocolVersion)
	}
	frames := admittedTurn(t, h, c, conversation, "show-card", "Show available appointment times.")
	var live protocol.Result
	count := 0
	for _, e := range frames {
		if p, ok := e.Payload.(protocol.Result); ok && p.Type == protocol.ResultTypeSurface {
			live = p
			count++
		}
		if p, ok := e.Payload.(protocol.ItemDone); ok && p.Text != nil && strings.Contains(*p.Text, "VALIDATION_FAILED") {
			t.Fatal("model validation error leaked to person")
		}
	}
	if count != 1 || live.Revision != "1" || live.Body == nil || live.Text == nil || !strings.Contains(*live.Text, "[a]") {
		t.Fatalf("partial/missing cards: %d %+v", count, live)
	}
	if calls.Load() != 3 {
		t.Fatalf("model calls=%d", calls.Load())
	}
	c.Close()
	h.stopCore(core)
	core = nil
	core = start(true)
	c, conversation = conversationClient(t, h, protocol.SurfaceResultsCapability())
	defer c.Close()
	receive := func(c *clientapi.Client, id string) protocol.Envelope {
		t.Helper()
		for {
			e, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if e.ReplyTo != nil && *e.ReplyTo == id {
				return e
			}
		}
	}
	read := func(revision json.Number) protocol.Result {
		t.Helper()
		id, err := c.Result(h.ctx, protocol.ResultGet{ResultID: live.ResultID, Revision: &revision, ConversationID: &conversation})
		if err != nil {
			t.Fatal(err)
		}
		e := receive(c, id)
		p, ok := e.Payload.(protocol.Result)
		if !ok || e.Error != nil {
			t.Fatalf("read=%s %+v", e.Kind, e.Error)
		}
		return p
	}
	restored := read("1")
	live.Stream = nil
	restored.Stream = nil
	if !reflect.DeepEqual(live, restored) {
		t.Fatalf("restart changed revision: live=%+v restored=%+v", live, restored)
	}
	if calls.Load() != 3 {
		t.Fatal("restart/read called provider")
	}
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=en_US.UTF-8"}, false, true, 100, 32)
	waitComposerReady(t, h, terminal)
	waitScreen(t, h, terminal, design.Text(design.English, "live"))
	waitScreen(t, h, terminal, "Dentist appointment")
	h.keys(terminal, "\x0f")
	snapshot := waitScreen(t, h, terminal, "이 시간으로 예약 요청")
	capture := screenText(snapshot)
	t.Logf("SURFACE TUI CAPTURE\n%s\nEND SURFACE TUI CAPTURE", capture)
	if path := os.Getenv("LINA_SURFACE_CAPTURE"); path != "" {
		if err := os.WriteFile(path, []byte(capture), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h.keys(terminal, "2a")
	waitScreen(t, h, terminal, "Saved your choice.")
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
	saved := <-records
	if saved.ID == "" {
		t.Fatal("no saved effect")
	}
	current := read("2")
	if current.Text == nil || !strings.Contains(*current.Text, "● 수 15:00") {
		t.Fatal("updated selection missing")
	}
	for _, tc := range []struct {
		revision json.Number
		values   map[string]json.RawMessage
		want     protocol.ErrorCode
	}{
		{"1", map[string]json.RawMessage{"slot": json.RawMessage(`["wed-1500"]`)}, protocol.ErrorStaleRevision},
		{"2", map[string]json.RawMessage{"slot": json.RawMessage(`["wed-1500"]`), "approval": json.RawMessage(`true`)}, protocol.ErrorMalformed},
	} {
		id, err := c.Control(h.ctx, protocol.SurfaceAction{ResultID: live.ResultID, Revision: tc.revision, Action: "choose", Values: tc.values, InputID: "invalid-" + string(tc.revision)})
		if err != nil {
			t.Fatal(err)
		}
		response := receive(c, id)
		if response.Error == nil || response.Error.Code != tc.want {
			t.Fatalf("action refusal: %+v", response)
		}
	}
	if calls.Load() != 6 {
		t.Fatalf("rejected action started a turn: %d", calls.Load())
	}
	export := h.command("export", "--socket", h.socket, "--agent", live.Provenance.AgentID, "--conversation", conversation, "--since", "2020-01-01T00:00:00Z", "--until", "2100-01-01T00:00:00Z")
	export.start(t)
	export.wait(t, 0)
	var exported protocol.ExportResult
	if err := json.Unmarshal(export.stdout.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	episodes, err := os.ReadFile(filepath.Join(exported.Directory, "episodes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"present_surface", "createSurface", "updateComponents", "wed-1500", live.ResultID} {
		if !strings.Contains(string(episodes), part) {
			t.Errorf("episode export lost %s", part)
		}
	}
	if calls.Load() != 6 {
		t.Fatal("export called provider")
	}

}
