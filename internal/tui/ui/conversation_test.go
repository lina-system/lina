// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"encoding/json"
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
	m.conversation.id = "main:fixture"
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

func TestConversationSendWaitsForInitialHistory(t *testing.T) {
	for _, text := range []string{"첫 메시지", "/queue 다음 메시지"} {
		t.Run(text, func(t *testing.T) {
			m := newShell(Options{Width: 80, Profile: Monochrome}, time.Now)
			m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation")})
			m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
			m.Update(tea.PasteMsg{Content: text})
			if m.conversation.id != "" || m.status.State != Live {
				t.Fatal("expected Welcome before initial History")
			}
			for range 2 {
				_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if cmd != nil || m.content.Draft.Text != text || len(m.content.Blocks) != 0 || m.content.Busy {
					t.Fatalf("send before History: command=%v draft=%q blocks=%d busy=%v", cmd != nil, m.content.Draft.Text, len(m.content.Blocks), m.content.Busy)
				}
			}
			// The matching History response supplies the destination. Discovery
			// alone must not send; the person explicitly presses Enter again.
			m.conversation.id = "main:fixture"
			m.agentDiscoveryDone()
			if m.content.Draft.Text != text || len(m.content.Blocks) != 0 {
				t.Fatal("discovery changed the retained draft")
			}
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if cmd == nil || m.content.Draft.Text != "" || len(m.content.Blocks) != 1 {
				t.Fatal("explicit send after History did not create one echo")
			}
			request := cmd().(shellEffect).Message.(state.SendRequest)
			wantMode := state.InputNormal
			if strings.HasPrefix(text, "/queue ") {
				wantMode = state.InputQueue
			}
			if request.Draft.Text != strings.TrimPrefix(text, "/queue ") || request.Mode != wantMode {
				t.Fatalf("retained send changed: %+v", request)
			}
		})
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

func TestConversationResultReplacesStreamWithCard(t *testing.T) {
	m := conversationShell()
	topic := "conversation:fixture"
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic}})
	project := func(kind protocol.Kind, payload protocol.Payload) {
		t.Helper()
		p, err := projectStream(protocol.Envelope{Epoch: "1", Kind: kind, Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		m.Update(shellData{Generation: 1, Message: p.message})
	}
	metadata := func(seq string) *protocol.StreamMetadata {
		return &protocol.StreamMetadata{Topic: topic, Key: "reply", Cursor: protocol.StreamCursor{Epoch: "1", Seq: json.Number(seq)}, Revision: json.Number(seq)}
	}
	partial := "완결된 문장. 미완성"
	project(protocol.KindOutputDelta, protocol.OutputDelta{Stream: metadata("1"), Text: &partial})
	full := partial + strings.Repeat(" 긴 내용", 100) + " DETAIL-END"
	project(protocol.KindResult, protocol.Result{Stream: metadata("2"), Title: "CARD-102", Summary: "완결된 문장.", Text: &full})
	view := m.View().Content
	if len(m.content.Blocks) != 1 || !strings.Contains(view, "완결된 문장.") || !strings.Contains(view, "CARD-102") || !strings.Contains(view, "Ctrl+O") || strings.Contains(view, "미완성") || strings.Contains(view, "DETAIL-END") {
		t.Fatalf("result did not replace the unfinished stream with a bounded card: %s", view)
	}
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.conversation.pager.text != full {
		t.Fatal("card lost full text in Details")
	}
}

func TestConversationGeneratedReplyInline(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		t.Run(fmt.Sprintf("snapshot=%t", snapshot), func(t *testing.T) {
			m := conversationShell()
			topic, key := "conversation:fixture", "reply"
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic}})
			full := "First sentence.\nMiddle sentence.\nLast sentence."
			card := protocol.Result{ResultID: "result:" + key, Type: protocol.ResultTypeDocumentDraft, Status: protocol.ResultStatusPreview,
				Title: "An arbitrary display title", Summary: "First sentence.", Text: &full,
				Provenance: protocol.ResultProvenance{ConversationID: "fixture"}}
			apply := func(seq string) {
				t.Helper()
				frame := protocol.Envelope{Epoch: "1", Kind: protocol.KindResult}
				cursor := protocol.StreamCursor{Epoch: "1", Seq: json.Number(seq)}
				if snapshot {
					// Snapshot payloads need not repeat stream metadata.
					data, err := json.Marshal(card)
					if err != nil {
						t.Fatal(err)
					}
					frame.Kind = protocol.KindSnapshot
					frame.Payload = protocol.Snapshot{Topic: topic, Cursor: cursor, RevocationEpoch: "1", Items: []protocol.StreamItem{{Key: key, Kind: protocol.KindResult, Revision: json.Number(seq), Payload: data}}}
				} else {
					card.Stream = &protocol.StreamMetadata{Topic: topic, Key: key, Cursor: cursor, Revision: json.Number(seq)}
					frame.Payload = card
				}
				projected, err := projectStream(frame)
				if err != nil {
					t.Fatal(err)
				}
				m.Update(shellData{Generation: 1, Message: projected.message})
			}
			apply("1")
			view := m.View().Content
			for _, line := range strings.Split(full, "\n") {
				if strings.Count(view, line) != 1 {
					t.Fatalf("reply must appear once inline: %s", view)
				}
			}
			if len(m.content.Blocks) != 1 || strings.Index(view, "Ctrl+O") < strings.Index(view, "Last sentence.") {
				t.Fatalf("pager affordance must follow the reply: %s", view)
			}
			full += "\nUpdated body with unchanged summary."
			apply("2")
			if !strings.Contains(m.View().Content, "Updated body with unchanged summary.") {
				t.Fatal("reply body update did not invalidate the rendered block")
			}
			m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if m.conversation.pager.text != full {
				t.Fatal("pager lost the full reply")
			}
		})
	}
}

func TestConversationStructuredResultsStayCards(t *testing.T) {
	for _, kind := range []protocol.ResultType{protocol.ResultTypeDocumentDraft, protocol.ResultTypeTable, protocol.ResultTypeComparison, protocol.ResultTypePlanView} {
		t.Run(string(kind), func(t *testing.T) {
			m := conversationShell()
			topic := "conversation:fixture"
			m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic}})
			body := "Structured detail stays in the pager."
			card := protocol.Result{ResultID: "explicit-result", Type: kind, Status: protocol.ResultStatusPreview,
				Title: "Full reply", Summary: "Structured summary.", Text: &body,
				Provenance: protocol.ResultProvenance{ConversationID: "fixture"},
				Stream:     &protocol.StreamMetadata{Topic: topic, Key: "reply", Cursor: protocol.StreamCursor{Epoch: "1", Seq: "1"}, Revision: "1"}}
			if kind != protocol.ResultTypeDocumentDraft {
				card.ResultID = "result:reply"
			}
			projected, err := projectStream(protocol.Envelope{Epoch: "1", Kind: protocol.KindResult, Payload: card})
			if err != nil {
				t.Fatal(err)
			}
			m.Update(shellData{Generation: 1, Message: projected.message})
			view := m.View().Content
			if !strings.Contains(view, card.Summary) || strings.Contains(view, body) {
				t.Fatalf("structured result expanded inline: %s", view)
			}
			m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
			if m.conversation.pager.text != body {
				t.Fatal("structured detail unavailable")
			}
		})
	}
}

func TestConversationOpenCardReceivesRemainingReply(t *testing.T) {
	m := conversationShell()
	topic := "conversation:fixture"
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "1", Revocation: "1", Topic: topic, Items: []state.Block{{ID: "card", Kind: protocol.KindResult, Text: "SUMMARY-102", Body: "PARTIAL-102", Done: true}}}})
	m.Update(tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	m.Update(shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: topic, Cursor: 1, Block: state.Block{ID: "card", Kind: protocol.KindResult, Text: "SUMMARY-102", Body: "PARTIAL-102\nEND-102", Revision: 1, Done: true}}})
	if !strings.Contains(m.View().Content, "END-102") {
		t.Fatal("open result retained an incomplete streaming preview")
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
