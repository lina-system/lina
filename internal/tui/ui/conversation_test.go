// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func conversationShell() *shellModel {
	m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
	m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	return m
}

func TestConversationComposerReceivesTerminalText(t *testing.T) {
	m := conversationShell()
	m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m.Update(tea.PasteMsg{Content: "한글\x1b]52;c;secret\a input"})
	if m.status.State != Live || m.content.Draft.Text != "q한글 input" {
		t.Fatalf("terminal text did not reach composer: state=%s draft=%q", m.status.State.Code(), m.content.Draft.Text)
	}
	if !strings.Contains(m.View().Content, "q한글 input") || m.View().Cursor == nil {
		t.Fatal("composer text and hardware cursor must be visible")
	}
}

func TestConversationSendClearsDraftAndCreatesOneEcho(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "MESSAGE-108"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || m.content.Draft.Text != "" || len(m.content.Blocks) != 1 || !m.content.Blocks[0].Local {
		t.Fatalf("send missing: cmd=%v draft=%q blocks=%d", cmd != nil, m.content.Draft.Text, len(m.content.Blocks))
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, IsRepeat: true})
	if len(m.content.Blocks) != 1 {
		t.Fatal("repeated Enter sent twice")
	}
}

func TestConversationReadOnlyRetainsDraft(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "KEEP-108"})
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Retrying}})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || m.content.Draft.Text != "KEEP-108" || len(m.content.Blocks) != 0 {
		t.Fatal("disconnected send changed the draft or issued an effect")
	}
}

func TestConversationDoneReplacesStreamAndUnchangedWritesNothing(t *testing.T) {
	m := conversationShell()
	topic := "conversation:fixture"
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic}})
	m.Update(shellData{Generation: 1, Message: state.Delta{Epoch: "1", Topic: topic, ID: "reply", Cursor: 1, Revision: 1, Text: "PARTIAL-108"}})
	finished := shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: topic, Cursor: 2, Block: state.Block{ID: "reply", Revision: 2, Text: "FINAL-108"}}}
	m.Update(finished)
	var output bytes.Buffer
	d := &display{output: &output, interactive: true, styles: m.styles}
	if err := d.showShell(m); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	m.Update(finished)
	if err := d.showShell(m); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || len(m.content.Blocks) != 1 || m.content.Blocks[0].Text != "FINAL-108" {
		t.Fatalf("duplicate completion changed content/output: blocks=%d bytes=%d", len(m.content.Blocks), output.Len())
	}
}

func TestConversationPagerShowsFullCardAndPreservesDraft(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "DRAFT-108"})
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "results:fixture", Items: []state.Block{{ID: "card", Kind: protocol.KindResult, Text: "SUMMARY-108", Body: "DETAIL-108\nSEARCH-108", Done: true}}}})
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if !strings.Contains(m.View().Content, "DETAIL-108") {
		t.Fatal("pager did not open the full text card")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.content.Draft.Text != "DRAFT-108" || m.content.Stopping {
		t.Fatal("closing pager changed draft or interrupted turn")
	}
}

func TestConversationEditingGateAndNewline(t *testing.T) {
	m := conversationShell()
	m.Update(tea.KeyReleaseMsg{Code: 'x', Text: "x"})
	m.Update(tea.KeyPressMsg{Code: 'y', Text: "y", IsRepeat: true})
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	m.Update(tea.PasteMsg{Content: "한글"})
	if m.content.Draft.Text != "y\n한글" {
		t.Fatalf("editing gate/newline: %q", m.content.Draft.Text)
	}
	m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m.content.Draft.Text != "" || m.status.State != Live {
		t.Fatal("clear draft quit")
	}
}

func TestConversationCanReadOlderMessagesAndOpenWidePager(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 24})
	var items []state.Block
	for i := 0; i < 50; i++ {
		items = append(items, state.Block{ID: fmt.Sprint(i), Text: fmt.Sprintf("ROW-%02d", i), Done: true})
	}
	items = append(items, state.Block{ID: "card", Kind: protocol.KindResult, Text: "CARD-108", Body: "BODY-108", Done: true})
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: "conversation:fixture", Items: items}})
	before := m.View().Content
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if m.View().Content == before {
		t.Fatal("conversation cannot scroll to older messages")
	}
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	view := m.View().Content
	if !strings.Contains(view, "BODY-108") || !strings.Contains(view, "ROW-") {
		t.Fatal("wide pager must retain the conversation beside it")
	}
}

func TestReconnectSameEpochRestoresUnacknowledgedDraft(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "UNACKNOWLEDGED"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m.Update(tea.PasteMsg{Content: "NEW"})
	m.Update(shellBegin{Generation: 2})
	if m.content.Busy || m.content.Draft.Text != "UNACKNOWLEDGED\nNEW" {
		t.Fatal("same-epoch disconnect left input pending or lost the draft")
	}
}

func TestConversationEpochNotice(t *testing.T) {
	for _, width := range []int{60, 80, 120} {
		for _, language := range []design.Language{design.English, design.Korean} {
			t.Run(fmt.Sprintf("%d/%s", width, language), func(t *testing.T) {
				m := conversationShell()
				options := m.styles.options
				options.Language = language
				m.styles = NewStyles(options)
				m.Update(tea.WindowSizeMsg{Width: width, Height: 60})
				snapshot := state.Snapshot{Epoch: "9", Revocation: "1", Topic: "conversation:fixture", Cursor: 1, Items: []state.Block{{ID: "history", Text: "HISTORY-359", Revision: 1, Done: true}}}
				m.Update(shellData{Generation: 1, Message: snapshot})
				m.Update(tea.PasteMsg{Content: "DRAFT-359"})
				if len(m.content.Notices) != 0 {
					t.Fatal("initial connection created a notice")
				}
				m.Update(shellBegin{Generation: 2})
				m.Update(shellData{Generation: 2, Message: snapshot})
				if len(m.content.Notices) != 0 {
					t.Fatal("same-epoch reconnect created a notice")
				}
				for index, epoch := range []string{"10", "11"} {
					// The connection owner and topic snapshots may both announce the
					// epoch. They must still produce one local notice per change.
					m.Update(shellData{Generation: 2, Message: state.Notice{ID: "epoch:" + epoch, Code: "epoch_changed"}})
					snapshot.Epoch = epoch
					m.Update(shellData{Generation: 2, Message: snapshot})
					m.Update(shellData{Generation: 2, Message: snapshot})
					m.Update(shellConnection{Generation: 2, Status: ConnectionStatus{State: Live}})
					view := m.View().Content
					if count := strings.Count(view, design.Text(language, "epoch_changed")); count != 1 {
						t.Fatalf("visible local notices = %d, want 1", count)
					}
					if len(m.content.Notices) != index+1 || len(m.content.Blocks) != 1 || !strings.Contains(view, "HISTORY-359") || !strings.Contains(view, "DRAFT-359") {
						t.Fatal("notice duplicated, entered history or hid history/draft")
					}
					if m.View().Content != view {
						t.Fatal("redraw changed the local notice")
					}
				}
				// An empty resync must retain the local notice outside the ledger.
				snapshot.Items, snapshot.Cursor = nil, 2
				m.Update(shellData{Generation: 2, Message: snapshot})
				if len(m.content.Blocks) != 0 || strings.Count(m.View().Content, design.Text(language, "epoch_changed")) != 1 {
					t.Fatal("empty history snapshot lost the local notice")
				}
			})
		}
	}
}

func TestPasteIsOneUndoStepAndComposerGrowsOnWrap(t *testing.T) {
	m := conversationShell()
	m.Update(tea.PasteMsg{Content: "KEEP"})
	m.Update(tea.PasteMsg{Content: strings.Repeat("한", 90)})
	if m.conversationPlan().Composer.Height < 3 {
		t.Fatal("wrapped draft did not grow the composer")
	}
	m.Update(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if m.content.Draft.Text != "KEEP" {
		t.Fatal("paste was not one undo step")
	}
}
