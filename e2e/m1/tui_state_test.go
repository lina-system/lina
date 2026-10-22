// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/fakecore"
	"github.com/thisisjun786/lina/internal/tui/state"
)

// The current wire schema carries handshakes, but reserves conversation bodies.
// Exercise the reducer's actual receive boundary across #34's isolated socket.
func TestM1TUIState(t *testing.T) {
	data, err := os.ReadFile("tui-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var script struct {
		Steps []fakecore.Step `json:"steps"`
	}
	if err := json.Unmarshal(data, &script); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "")
	done := h.fakeCore(script.Steps)
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var states []state.State
	var s state.State
	for scanner.Scan() {
		var frame struct {
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &frame); err != nil {
			t.Fatal(err)
		}
		next, cmds := state.Reduce(s, state.Frame{Data: frame.Params})
		if len(cmds) != 0 || next.Malformed != 0 || next.Unknown != 0 || len(next.Blocks) != 0 {
			t.Fatal("invalid receive state")
		}
		states = append(states, next)
		s = next
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(states) != 6 || states[0].Epoch != "42" || states[4].Epoch != "43" {
		t.Fatalf("states: %+v", states)
	}
	for _, pair := range [][2]int{{0, 1}, {1, 2}, {2, 3}, {4, 5}} {
		if !reflect.DeepEqual(states[pair[0]], states[pair[1]]) {
			t.Fatal("duplicate or reserved payload changed state")
		}
	}
	notices := make(map[string]int)
	for _, notice := range s.Notices {
		notices[notice.Code]++
	}
	if !reflect.DeepEqual(notices, map[string]int{"epoch_changed": 1, "shell.lists_unavailable": 1}) {
		t.Fatalf("epoch or capability notice not deduplicated: %v", notices)
	}
}
