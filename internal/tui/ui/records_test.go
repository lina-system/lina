// SPDX-License-Identifier: Apache-2.0
package ui

import (
	tea "charm.land/bubbletea/v2"
	"context"
	"github.com/thisisjun786/lina/kit/protocol"
	"strings"
	"testing"
)

func TestUnsupportedRecordCommandsNeverBecomeChat(t *testing.T) {
	for _, command := range []string{"/records", "/memos", "/add 책 반납"} {
		t.Run(command, func(t *testing.T) {
			m := conversationShell()
			m.Update(tea.PasteMsg{Content: command})
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd != nil || len(m.content.Blocks) != 0 || !m.conversation.pager.open {
				t.Fatal("record command became conversation input")
			}
		})
	}
}

func TestRecordBrowserUsesCoreOrderAndOpaqueTargets(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "records", Version: "1.0"})
	m.content.Draft.Text = "retain this draft"
	handled, cmd := m.recordsCommand("/records memos 연필")
	if !handled || cmd == nil {
		t.Fatal("missing query")
	}
	request := cmd().(shellManagement).Request.(protocol.RecordRequest)
	if request.Operation != "query" || request.View == nil || *request.View != "memos" || request.Search == nil || *request.Search != "연필" {
		t.Fatal("wrong Core query", request)
	}
	m.conversation.pager.recordID = "query"
	rows := []protocol.RecordRow{{Ref: "z-opaque", Revision: "z-revision", Kind: "memo", State: "active", Title: "Zebra", Note: "", Chips: []protocol.RecordChip{}}, {Ref: "a-opaque", Revision: "a-revision", Kind: "memo", State: "active", Title: "Apple", Note: "", Chips: []protocol.RecordChip{}}}
	reply := "query"
	session := managementSession{model: m, pending: map[string]protocol.Payload{reply: request}}
	_, err := session.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.RecordResult{Operation: "query", Rows: rows, Next: stringPointer("cursor")}})
	if err != nil {
		t.Fatal(err)
	}
	text := m.conversation.pager.text
	if strings.Index(text, "Zebra") > strings.Index(text, "Apple") || strings.Contains(text, "opaque") || strings.Contains(text, "revision") {
		t.Fatal("client reordered rows or exposed routing data", text)
	}
	_, cmd = m.recordsKey("enter")
	read := cmd().(shellManagement).Request.(protocol.RecordRequest)
	if read.Operation != "read" || read.Ref == nil || *read.Ref != "z-opaque" {
		t.Fatal("wrong selected record", read)
	}
	if m.content.Draft.Text != "retain this draft" {
		t.Fatal("browser changed draft")
	}
}

func TestRecordReplyAfterCloseCannotReplaceAnotherPager(t *testing.T) {
	m := conversationShell()
	m.openPager("new pager")
	reply := "old"
	session := managementSession{model: m, pending: map[string]protocol.Payload{reply: protocol.RecordRequest{Operation: "query"}}}
	_, err := session.response(context.Background(), nil, protocol.Envelope{ReplyTo: &reply, Payload: protocol.RecordResult{Operation: "query", Rows: []protocol.RecordRow{}}})
	if err != nil || m.conversation.pager.text != "new pager" {
		t.Fatal("late record reply replaced current view", err)
	}
}

func TestQuickAddShowsCoreChipsBeforeExplicitSave(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "records", Version: "1.0"})
	handled, cmd := m.recordsCommand("/add 책 반납 2026-10-25")
	if !handled || cmd == nil {
		t.Fatal("missing preview request")
	}
	request := cmd().(shellManagement).Request.(protocol.RecordRequest)
	if request.Operation != "preview" || request.Token != nil {
		t.Fatal("capture saved without preview")
	}
	row := protocol.RecordRow{Title: "책 반납", Kind: "todo", Chips: []protocol.RecordChip{{Kind: "when", Value: "2026-10-25"}, {Kind: "tag", Value: "심부름"}}}
	if err := m.recordResponse(request, protocol.RecordResult{Operation: "preview", Rows: []protocol.RecordRow{row}, Token: stringPointer("opaque-preview")}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.conversation.pager.text, "2026-10-25") || !strings.Contains(m.conversation.pager.text, "심부름") || strings.Contains(m.conversation.pager.text, "opaque-preview") {
		t.Fatal("missing or unsafe preview", m.conversation.pager.text)
	}
	_, cmd = m.recordsKey("enter")
	save := cmd().(shellManagement).Request.(protocol.RecordRequest)
	if save.Operation != "save" || save.Token == nil || *save.Token != "opaque-preview" || save.Text != nil {
		t.Fatal("confirmation changed preview", save)
	}
	_, repeat := m.recordsKey("enter")
	if repeat != nil {
		t.Fatal("repeated key submitted second save")
	}
	m.Update(shellBegin{Generation: 2})
	if m.conversation.pager.open {
		t.Fatal("reconnect retained capture")
	}
}
