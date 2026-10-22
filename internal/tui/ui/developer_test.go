// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestUnsupportedDeveloperNeverBecomesChat(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "/developer"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || len(m.content.Blocks) != 0 || !m.conversation.pager.open || !strings.Contains(m.conversation.pager.text, "capability") {
		t.Fatal("unsupported developer command became model input instead of a local capability refusal")
	}
}

func TestDeveloperLanesFoldAndRefresh(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "worker_lanes", Version: "1.0"})
	handled, cmd := m.developerCommand("/developer")
	if !handled || cmd == nil {
		t.Fatal("developer request missing")
	}
	if _, ok := cmd().(shellManagement).Request.(protocol.ProgressGet); !ok {
		t.Fatal("wrong protocol request")
	}
	rows := []protocol.WorkerLane{
		{LaneID: "one", TaskID: "a", AgentID: "agent", AgentName: "LINA", Worker: "Codex", Title: "first\nextra", State: "in_progress", ThreadID: "thread-one", Details: []string{"worker (worker_report): ready_for_review"}},
		{LaneID: "two", TaskID: "b", AgentID: "agent", AgentName: "LINA", Worker: "Codex", Title: strings.Repeat("long", 90), ThreadID: "thread-two"},
	}
	p := &m.conversation.pager
	p.lanes = p.lanes.Replace(rows)
	m.refreshDeveloper()
	if strings.Count(p.text, "[Codex]") != 2 || strings.Contains(p.text, "thread-one") || strings.Contains(p.text, "worker_report") {
		t.Fatalf("bad folded view: %s", p.text)
	}
	for _, line := range strings.Split(p.text, "\n") {
		if strings.Contains(line, "[Codex]") && (render.Unicode{}).Width(line) > m.size.Width-2 {
			t.Fatal("lane wraps")
		}
	}
	m.developerKey("enter")
	if !strings.Contains(p.text, "thread-one") || !strings.Contains(p.text, "worker_report") || strings.Contains(p.text, "thread-two") {
		t.Fatalf("wrong expansion: %s", p.text)
	}
	m.developerKey("down")
	m.developerKey("enter")
	p.lanes = p.lanes.Replace(rows)
	m.refreshDeveloper()
	if !strings.Contains(p.text, "thread-one") || !strings.Contains(p.text, "thread-two") {
		t.Fatal("refresh lost expansions")
	}
	m.developerKey("enter")
	if strings.Contains(p.text, "thread-two") {
		t.Fatal("fold did not hide detail")
	}
	m.Update(shellBegin{Generation: 2})
	if m.conversation.pager.open {
		t.Fatal("reconnect retained old worker authority")
	}
}

func TestDeveloperReplyAfterCloseIsIgnored(t *testing.T) {
	m := conversationShell()
	request := protocol.ProgressGet{}
	s := managementSession{model: m, pending: map[string]protocol.Payload{"old": request}}
	m.openPager("new view")
	lanes := []protocol.WorkerLane{{LaneID: "old"}}
	reply := "old"
	handled, err := s.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.Progress{Lanes: &lanes}})
	if !handled || err != nil || m.conversation.pager.text != "new view" {
		t.Fatal("stale reply replaced view")
	}
}
