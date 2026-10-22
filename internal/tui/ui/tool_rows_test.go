// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func rowEvent(t *testing.T, seq int, call, name, status, outcome, target, summary string) protocol.Envelope {
	t.Helper()
	body := map[string]any{"stream": map[string]any{"topic": toolTopic, "key": "tool:" + call, "revision": seq, "cursor": map[string]any{"epoch": "1", "seq": seq}}, "call_id": call, "tool_name": name, "status": status}
	if status == "finished" {
		body["outcome"], body["duration_ms"] = outcome, 25
	}
	if target != "" {
		body["target"] = target
	}
	if summary != "" {
		body["summary"] = summary
	}
	raw, _ := json.Marshal(body)
	payload, err := streamItemPayload(protocol.StreamItem{Kind: protocol.Kind("tool_activity"), Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	return protocol.Envelope{Kind: protocol.Kind("tool_activity"), Epoch: "1", Payload: payload}
}

func TestToolRowsLifecycleAndGrouping(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
	before := len(m.content.Blocks)
	event := rowEvent(t, 2, "one", "Reading file", "started", "", "", "")
	applyToolProjection(t, m, event)
	view := m.View().Content
	if !strings.Contains(view, "Reading file") || !strings.Contains(view, "running") {
		t.Fatalf("missing running tool row: %q", view)
	}
	accepted := m.content
	applyToolProjection(t, m, event)
	if !reflect.DeepEqual(accepted, m.content) {
		t.Fatal("replay changed tool rows")
	}
	applyToolProjection(t, m, rowEvent(t, 3, "one", "Reading file", "finished", "ok", "", ""))
	if view = m.View().Content; !strings.Contains(view, "ok") || strings.Contains(view, "running  Reading file") {
		t.Fatalf("missing completed row: %q", view)
	}
	applyToolProjection(t, m, rowEvent(t, 4, "two", "Reading file", "started", "", "", ""))
	applyToolProjection(t, m, rowEvent(t, 5, "two", "Reading file", "finished", "failed", "", ""))
	view = m.View().Content
	if strings.Count(view, "Reading file") != 1 || !strings.Contains(view, "×2") || !strings.Contains(view, "failed") {
		t.Fatalf("group lost count/failure: %q", view)
	}
	applyToolProjection(t, m, rowEvent(t, 6, "three", "Listing skills", "finished", "ok", "", ""))
	applyToolProjection(t, m, rowEvent(t, 7, "four", "Reading file", "finished", "ok", "", ""))
	if strings.Count(m.View().Content, "Reading file") != 2 {
		t.Fatal("non-consecutive calls grouped")
	}
	if len(m.content.Blocks) != before || len(m.content.Worked) != 0 {
		t.Fatal("tool rows became conversation blocks or completion metadata")
	}
	m.Update(shellData{Generation: 1, Message: state.FocusConversation{}})
	if strings.Contains(m.View().Content, "Reading file") {
		t.Fatal("tool rows survived focus change")
	}
}

func TestToolRowsSummaryAndASCII(t *testing.T) {
	m := toolShell(t)
	m.styles = NewStyles(Options{Language: design.English, ASCII: true})
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
	summary := "first\nsecond\nthird\nfourth\nfifth"
	applyToolProjection(t, m, rowEvent(t, 2, "one", "Reading\x1b]52;c;PRIVATE\a file", "finished", "failed", "target\nname", summary))
	view := m.View().Content
	for _, want := range []string{"x failed", "Reading file", "target name", "first", "third", "+2 lines", "Ctrl+O"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q: %q", want, view)
		}
	}
	for _, hidden := range []string{"PRIVATE", "fourth", "fifth"} {
		if strings.Contains(view, hidden) {
			t.Errorf("unfolded/unsafe text %q", hidden)
		}
	}
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if view = m.View().Content; !strings.Contains(view, "fourth") || !strings.Contains(view, "fifth") {
		t.Fatalf("Ctrl+O did not open tool output: %q", view)
	}
}

func TestToolRowsKeepProseAndClearRevokedData(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
	applyToolProjection(t, m, rowEvent(t, 2, "one", "Reading file", "finished", "ok", "", ""))
	text := ""
	for i := 0; i < 6; i++ {
		text += fmt.Sprintf("prose-%d\n", i)
	}
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: toolTopic, Cursor: 3, Block: state.Block{ID: "reply", Kind: protocol.KindItemDone, Speaker: protocol.ConversationSpeakerAgent, Revision: 1, Text: text}}})
	view := m.View().Content
	for i := 0; i < 6; i++ {
		if !strings.Contains(view, fmt.Sprintf("prose-%d", i)) {
			t.Fatal("assistant prose folded")
		}
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "2", Topic: toolTopic, Cursor: 4}})
	if strings.Contains(m.View().Content, "Reading file") {
		t.Fatal("revoked tool display survived")
	}
}

func TestToolRowsGapTurnIdentityAndPagerInvalidation(t *testing.T) {
	m := toolShell(t)
	applyToolProjection(t, m, toolTurn(t, 1, "tool", "Reading"))
	applyToolProjection(t, m, rowEvent(t, 3, "gap", "GAP", "started", "", "", ""))
	if strings.Contains(m.View().Content, "GAP") {
		t.Fatal("gap event rendered")
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: toolTopic, Cursor: 3}})
	applyToolProjection(t, m, rowEvent(t, 4, "one", "Reading file", "finished", "ok", "", "private summary"))
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(m.View().Content, "private summary") {
		t.Fatal("summary not open")
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "2", Topic: toolTopic, Cursor: 5}})
	if strings.Contains(m.View().Content, "private summary") {
		t.Fatal("revoked summary remains in pager")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	applyToolProjection(t, m, toolTurn(t, 6, "tool", "Reading"))
	applyToolProjection(t, m, rowEvent(t, 7, "reused", "Reading file", "finished", "ok", "", ""))
	m.Update(shellData{Generation: 1, Message: state.Turn{Active: false}})
	applyToolProjection(t, m, toolTurn(t, 8, "tool", "Reading"))
	applyToolProjection(t, m, rowEvent(t, 9, "reused", "Reading file", "started", "", "", ""))
	if len(m.content.ToolCalls) != 2 || !strings.Contains(m.View().Content, "running  Reading file") {
		t.Fatal("call id reused in next turn was suppressed")
	}
}
