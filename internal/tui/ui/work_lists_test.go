// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func workListWelcome(t *testing.T, capabilities ...string) state.Frame {
	t.Helper()
	var envelope protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "welcome"), &envelope); err != nil {
		t.Fatal(err)
	}
	welcome := envelope.Payload.(protocol.Welcome)
	welcome.Capabilities = []protocol.Capability{}
	for _, name := range capabilities {
		welcome.Capabilities = append(welcome.Capabilities, protocol.Capability{Name: name, Version: "1.0"})
	}
	envelope.Payload = welcome
	return streamFrame(t, envelope)
}

func TestWorkListsFollowWelcomeCapabilities(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, tc := range []struct {
			name   string
			caps   []string
			notice string
		}{
			{"both available", []string{"conversation", "running", "waiting"}, ""},
			{"both missing", []string{"conversation"}, "shell.lists_unavailable"},
			{"running missing", []string{"conversation", "waiting"}, "shell.running_unavailable"},
			{"waiting missing", []string{"conversation", "running"}, "shell.waiting_unavailable"},
		} {
			t.Run(string(language)+"/"+tc.name, func(t *testing.T) {
				m := newShell(Options{Width: 100, Profile: TrueColor, Language: language}, time.Now)
				if len(m.content.Notices) != 0 {
					t.Fatal("capability notice before negotiation")
				}
				welcome := workListWelcome(t, tc.caps...)
				m.Update(shellData{Generation: 1, Message: welcome})
				m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
				plan := m.conversationPlan()
				if plan.Running.Height != 0 || plan.Cards.Height != 0 {
					t.Errorf("empty/unavailable lists remain visible: %+v", plan)
				}
				if tc.notice == "" {
					if len(m.content.Notices) != 0 {
						t.Fatal("empty granted lists presented as unavailable")
					}
				} else {
					text, ok := design.Lookup(language, tc.notice)
					if !ok || len(m.content.Notices) != 1 || m.content.Notices[0].Code != tc.notice || !strings.Contains(m.View().Content, text) {
						t.Fatalf("missing localized capability notice: %+v; screen=%s", m.content.Notices, m.View().Content)
					}
				}
				before := len(m.content.Notices)
				m.Update(shellData{Generation: 1, Message: welcome})
				m.Update(shellBegin{Generation: 2})
				m.Update(shellData{Generation: 2, Message: welcome})
				m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
				if len(m.content.Notices) != before || len(m.content.Blocks) != 0 {
					t.Fatal("reconnect duplicated notice or placed it in history")
				}
				var output bytes.Buffer
				d := &display{output: &output, interactive: true, styles: m.styles}
				if err := d.showShell(m); err != nil {
					t.Fatal(err)
				}
				output.Reset()
				if err := d.showShell(m); err != nil {
					t.Fatal(err)
				}
				if output.Len() != 0 {
					t.Fatal("unchanged view repeated the notice")
				}
			})
		}
	}
}

func TestWorkListsAppearAndDisappearWithSnapshots(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running", "waiting")})
	m.Update(tea.PasteMsg{Content: "KEEP-DRAFT"})
	for _, topic := range []string{"running", "waiting"} {
		snapshot := state.Snapshot{Epoch: "42", Revocation: "1", Topic: topic, Cursor: 1,
			Items: []state.Block{{ID: topic + "-1", Text: "FIRST-" + topic, Done: true}, {ID: topic + "-2", Text: "SECOND-" + topic, Done: true}}}
		m.Update(shellData{Generation: 1, Message: snapshot})
		view := m.View().Content
		if first, second := strings.Index(view, "FIRST-"+topic), strings.Index(view, "SECOND-"+topic); first < 0 || second < first {
			t.Fatalf("Core order lost: %s", view)
		}
		snapshot.Cursor++
		snapshot.Items = nil
		m.Update(shellData{Generation: 1, Message: snapshot})
	}
	plan := m.conversationPlan()
	if plan.Running.Height != 0 || plan.Cards.Height != 0 || m.content.Draft.Text != "KEEP-DRAFT" || m.conversation.pager.open {
		t.Fatal("clearing lists left rows or changed draft/focus")
	}
}

func TestWorkListNoticePrintedOnceInPlainTerminal(t *testing.T) {
	m := newShell(Options{Width: 100, Profile: Monochrome}, time.Now)
	welcome := workListWelcome(t, "conversation")
	m.Update(shellData{Generation: 1, Message: welcome})
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	var output bytes.Buffer
	d := &display{output: &output, interactive: true, styles: m.styles}
	if err := d.showShell(m); err != nil {
		t.Fatal(err)
	}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "1", Topic: "conversation:test", Cursor: 1,
		Items: []state.Block{{ID: "reply", Text: "REPLY", Done: true}}}})
	if err := d.showShell(m); err != nil {
		t.Fatal(err)
	}
	text := design.Text(design.English, "shell.lists_unavailable")
	if strings.Count(output.String(), text) != 1 {
		t.Fatalf("capability notice repeated on transcript update: %s", output.String())
	}
}

func TestWorkListNoticeRemovedWhenCapabilitiesGranted(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation")})
	m.Update(shellBegin{Generation: 2})
	m.Update(shellData{Generation: 2, Message: workListWelcome(t, "conversation", "running", "waiting")})
	m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
	if m.localNotice() != "" {
		t.Fatal("old missing-capability notice survived a grant")
	}
}

func TestRunningRemovalUpdatesOpenPagerAndKeepsDraft(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running")})
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "1", Topic: "running", Cursor: 1,
		Items: []state.Block{{ID: streamKey("running", "one"), RunID: "one", Text: "FIRST", Kind: protocol.KindRunningUpdate, Revision: 1}, {ID: streamKey("running", "two"), RunID: "two", Text: "SECOND", Kind: protocol.KindRunningUpdate, Revision: 1}}}})
	m.content.Draft.Text = "KEEP-DRAFT"
	m.openRunning()
	id, removed := "one", true
	p, err := projectStream(protocol.Envelope{Epoch: "42", Kind: protocol.KindRunningUpdate, Payload: protocol.RunningUpdate{RunID: &id, Removed: &removed,
		Stream: &protocol.StreamMetadata{Topic: "running", Key: "one", Cursor: protocol.StreamCursor{Epoch: "42", Seq: "2"}, Revision: "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(shellData{Generation: 1, Message: p.message})
	if len(m.topicBlocks("running")) != 1 || strings.Contains(m.conversation.pager.text, "FIRST") || !strings.Contains(m.conversation.pager.text, "SECOND") || m.content.Draft.Text != "KEEP-DRAFT" || !m.conversation.pager.open {
		t.Fatal("removal retained row or changed focus/draft")
	}
	m.Update(shellData{Generation: 1, Message: p.message}) // A replay cannot remove another row.
	if len(m.topicBlocks("running")) != 1 {
		t.Fatal("duplicate removal changed list")
	}
}
