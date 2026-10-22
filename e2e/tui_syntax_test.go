// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUISyntaxInstalledCore(t *testing.T) {
	data, err := os.ReadFile("m1/tui-syntax.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Input, Open, Close, Code, Finished string }
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"dark", "light", "high-contrast", "no-color"} {
		t.Run(mode, func(t *testing.T) {
			finish := make(chan struct{})
			var calls atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var body struct{ Input []json.RawMessage }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, body.Input)), scenario.Input) {
					t.Error("input did not reach provider")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Open)
				w.(http.Flusher).Flush()
				select {
				case <-finish:
				case <-r.Context().Done():
					return
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"delta\":%q}\n\n", scenario.Close)
				emitResponse(w, scenario.Open+scenario.Close, true)
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			core := start(true)
			env := []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LINA_THEME=" + mode}
			if mode == "no-color" {
				env = append(env, "NO_COLOR=1")
			}
			socket := h.socket
			tap := newAccountTap(t, h, socket, true)
			h.socket = tap.socket
			terminal := h.startTerminalSize(env, false, true, 100, 34)
			h.socket = socket
			defer func() {
				h.keys(terminal, "\x04")
				terminal.process.wait(t, 0)
				h.expectTerminalRestored(terminal)
				h.stopCore(core)
			}()
			if mode == "no-color" {
				waitPlainComposerReady(t, h, terminal)
			} else {
				waitComposerReady(t, h, terminal)
			}
			for {
				frame := tap.next(t, h)
				if s, ok := frame.Payload.(protocol.Snapshot); ok && strings.HasPrefix(s.Topic, "conversation:") {
					break
				}
			}
			h.keys(terminal, scenario.Input+"\r")
			open := waitScreen(t, h, terminal, scenario.Code)
			assertSyntaxCells(t, open, scenario.Code, false)
			close(finish)
			for {
				frame := tap.next(t, h)
				if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
					break
				}
			}
			closed := waitScreen(t, h, terminal, scenario.Finished)
			if mode == "no-color" {
				if strings.Contains(string(terminal.screen.Bytes()), "\x1b") {
					t.Fatal("NO_COLOR emitted escapes")
				}
			} else {
				assertSyntaxCells(t, closed, scenario.Code, mode != "high-contrast")
			}
			if calls.Load() != 1 {
				t.Fatalf("provider calls = %d", calls.Load())
			}
		})
	}
}

func assertSyntaxCells(t *testing.T, snapshot tuiterm.Snapshot, code string, highlighted bool) {
	t.Helper()
	for _, row := range snapshot.Cells {
		var line strings.Builder
		for _, cell := range row {
			line.WriteString(cell.Content)
		}
		if x := strings.Index(line.String(), code); x >= 0 {
			// The fixture and its prefix occupy one cell per rune.
			x = len([]rune(line.String()[:x]))
			keyword, name := row[x].Foreground, row[x+4].Foreground
			if got := !reflect.DeepEqual(keyword, name); got != highlighted {
				t.Errorf("syntax highlighted=%t, want %t; keyword=%v identifier=%v", got, highlighted, keyword, name)
			}
			return
		}
	}
	t.Fatalf("code absent from cells: %s", screenText(snapshot))
}
