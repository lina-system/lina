// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/tui/layout"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func cardModel(t *testing.T) *shellModel {
	t.Helper()
	m := newShell(Options{Width: 100}, func() time.Time { return time.Unix(10, 0) })
	m.size = layout.Size{Width: 100, Height: 30}
	m.status.State = Live
	m.content.Epoch = "1"
	c := state.Card{ID: "approval", Epoch: "1", Revision: 1, Token: "revision", Kind: protocol.KindApprovalRequest, Agent: "Agent", Summary: "Send", Targets: []state.CardField{{Name: "recipient", Value: "recipient@example.invalid"}}, Choices: []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceDeny}}
	m.content, _ = state.Reduce(m.content, state.PutCard{Card: c})
	m.content, _ = state.Reduce(m.content, state.OpenCard{ID: c.ID})
	return m
}
func TestCardPressOnlySeparateConfirmAndEscape(t *testing.T) {
	m := cardModel(t)
	for _, event := range []term.KeyEvent{term.Repeat, term.Release} {
		cmd, handled := m.cardKey(term.Key{Name: "1", Events: true, Event: event})
		if cmd != nil || !handled || m.content.CardView.Choice != "" {
			t.Fatal("non-press selected")
		}
	}
	cmd, _ := m.cardKey(term.Key{Name: "1", Events: true})
	if cmd != nil || m.content.CardView.Choice != protocol.ApprovalChoiceOnce {
		t.Fatal("selection sent or failed")
	}
	cmd, _ = m.cardKey(term.Key{Name: "enter", Events: true})
	if cmd == nil {
		t.Fatal("separate confirm missing")
	}
	if _, ok := cmd().(shellCardAnswer); !ok {
		t.Fatal("wrong effect")
	}
	m = cardModel(t)
	m.cardKey(term.Key{Name: "1", Events: true})
	cmd, _ = m.cardKey(term.Key{Name: "esc", Events: true})
	if cmd != nil || m.content.CardView.ID != "" {
		t.Fatal("escape decided")
	}
}
func TestCardCutTargetsAndConnectionStatesDisableConfirm(t *testing.T) {
	m := cardModel(t)
	m.content.Cards[0].Targets[0].Value = strings.Repeat("한글", 1000)
	m.cardKey(term.Key{Name: "1", Events: true})
	cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true})
	if cmd != nil {
		t.Fatal("cut target confirmed")
	}
	for _, status := range []Connection{Retrying, Resyncing, Recovering, Failed} {
		m = cardModel(t)
		m.status.State = status
		m.cardKey(term.Key{Name: "1", Events: true})
		cmd, _ = m.cardKey(term.Key{Name: "enter", Events: true})
		if cmd != nil {
			t.Fatalf("mutation while %v", status)
		}
	}
}
func TestCardLegacyBarrierAndPasteCannotApprove(t *testing.T) {
	m := cardModel(t)
	m.cardGate.CardChanged(m.now())
	m.cardKey(term.Key{Name: "1"})
	if m.content.CardView.Choice != "" {
		t.Fatal("legacy barrier missed")
	}
	m.Update(tea.PasteMsg{Content: "1\n/approve"})
	if m.content.CardView.Choice != "" || m.content.CardView.Pending {
		t.Fatal("paste approved")
	}
	m.now = func() time.Time { return time.Unix(10, 0).Add(term.ApprovalDeadTime) }
	m.cardKey(term.Key{Name: "1"})
	if m.content.CardView.Choice == "" {
		t.Fatal("legacy key remained blocked")
	}
}

func TestWireCardProjectionAndReplayBell(t *testing.T) {
	m := newShell(Options{Width: 100}, nil)
	m.content.Epoch = "1"
	m.focused = false
	c := protocol.DecisionCard{ID: "card", Revision: "group", Agent: "Agent", Summary: "Send", Targets: []protocol.DecisionTarget{{Name: "target", Value: "document"}}}
	frame := func(seq string) shellData {
		envelope := protocol.Envelope{Protocol: protocol.Protocol{Name: "client", Version: "1.0"}, Kind: protocol.KindApprovalRequest, MessageID: "frame", AttemptID: "attempt", Sender: protocol.Sender{Party: protocol.PartyCore, ID: "main"}, Epoch: "1", SentAt: "2026-01-01T00:00:00Z", EffectState: protocol.EffectNone, SourceRefs: []protocol.SourceRef{}, Payload: protocol.ApprovalRequest{Choices: []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceDeny}, Card: &c, Stream: &protocol.StreamMetadata{Topic: "waiting", Key: "card", Revision: json.Number(seq), Cursor: protocol.StreamCursor{Epoch: "1", Seq: json.Number(seq)}}}}
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return shellData{Generation: m.generation, Message: state.Frame{Data: data}}
	}
	_, cmd := m.Update(frame("1"))
	if len(m.content.Cards) != 1 || cmd == nil {
		t.Fatal("wire card or notification missing")
	}
	_, cmd = m.Update(frame("1"))
	if cmd != nil {
		t.Fatal("replayed cursor rang")
	}
	_, cmd = m.Update(frame("2"))
	if cmd != nil {
		t.Fatal("same approval republished and rang")
	}
	if m.content.CardView.ID != "" {
		t.Fatal("approval stole focus")
	}
}

func TestCardBellReplayThroughBubbleTeaWriter(t *testing.T) {
	data, err := os.ReadFile("../../../e2e/m1/tui-cards.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Replay struct{ Deliveries, Bells int }
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	if scenario.Replay.Deliveries < 2 || scenario.Replay.Bells != 1 {
		t.Fatal("invalid replay scenario")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	m := newShell(Options{Width: 80, Notification: "bell"}, nil)
	m.content.Epoch = "1"
	m.focused = false
	h, err := term.Start(ctx, m, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	card := state.Card{ID: "approval", Epoch: "1", Revision: 1, Kind: protocol.KindApprovalRequest, Agent: "Agent", Summary: "Send", Choices: []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceDeny}}
	event := shellData{Generation: 1, Message: state.ItemDone{Epoch: "1", Topic: "waiting", Cursor: 1, Notify: true, Block: state.Block{ID: "approval-block", Revision: 1, Card: &card}}}
	h.Send(event)
	if _, err := h.Output.Wait(ctx, func(term.Snapshot) bool { return bytes.Count(h.Output.Bytes(), []byte{'\a'}) == scenario.Replay.Bells }); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < scenario.Replay.Deliveries; i++ {
		h.Send(event)
	}
	h.Send(tea.KeyPressMsg{Code: 'q'})
	if _, err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := bytes.Count(h.Output.Bytes(), []byte{'\a'}); got != scenario.Replay.Bells {
		t.Fatalf("bells=%d", got)
	}
}

func TestCardReplyMustMatchTheSentCardAndAnswerKind(t *testing.T) {
	for _, fault := range []string{"card", "revision", "kind", "choice"} {
		t.Run(fault, func(t *testing.T) {
			m := cardModel(t)
			m.cardKey(term.Key{Name: "1", Events: true})
			cmd, _ := m.cardKey(term.Key{Name: "enter", Events: true})
			request := cmd().(shellCardAnswer).Request
			id := "request"
			m.cardRequests[id] = request
			card, revision := request.ID, request.Token
			outcome := protocol.DecisionOutcomeResolved
			answer := protocol.ApprovalAnswer{CardID: &card, Revision: &revision, Choice: request.Choice, Outcome: &outcome}
			envelope := protocol.Envelope{Kind: protocol.KindApprovalAnswer, ReplyTo: &id, Payload: answer}
			switch fault {
			case "card":
				card = "different"
			case "revision":
				revision = "different"
			case "choice":
				answer.Choice = protocol.ApprovalChoiceDeny
				envelope.Payload = answer
			case "kind":
				envelope.Kind = protocol.KindQuestionAnswer
				envelope.Payload = protocol.QuestionAnswer{CardID: &card, Revision: &revision, Answer: protocol.QuestionAnswerActionDecline, Outcome: &outcome}
			}
			if !m.cardReply(envelope) || m.status.State != Failed || m.status.Cause != protocol.ErrorMalformed || m.content.Cards[0].Outcome != "" {
				t.Fatal("mismatched response resolved card")
			}
		})
	}
}

func TestQuestionPasteEditsOnlyTheFocusedPrompt(t *testing.T) {
	m := cardModel(t)
	m.content.Cards[0].Kind = protocol.KindQuestion
	m.content.Cards[0].Choices = nil
	m.content.Cards[0].Form = &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "note", Type: protocol.FormFieldTypeText}, {Name: "count", Type: protocol.FormFieldTypeNumber}}}
	_, cmd := m.Update(tea.PasteMsg{Content: "한글\n/approve"})
	if cmd != nil || m.content.CardView.Fields["note"] != "한글\n/approve" || m.content.CardView.Pending || m.content.CardView.Choice != "" {
		t.Fatal("paste failed to edit or became a decision")
	}
	m.status.State = Retrying
	m.Update(tea.PasteMsg{Content: "later"})
	if m.content.CardView.Fields["note"] != "한글\n/approve" {
		t.Fatal("read-only question changed")
	}
}

func TestQuestionIMETextUsesTheWholeTypedEvent(t *testing.T) {
	m := cardModel(t)
	m.content.Cards[0].Kind = protocol.KindQuestion
	m.content.Cards[0].Form = &protocol.QuestionForm{PluginName: "Plugin", Fields: []protocol.FormField{{Name: "note", Type: protocol.FormFieldTypeText}}}
	m.Update(tea.KeyPressMsg{Code: '한', Text: "한글"})
	if m.content.CardView.Fields["note"] != "한글" {
		t.Fatal("IME text was reduced to one codepoint")
	}
	m.Update(tea.KeyReleaseMsg{Code: '한', Text: "한글"})
	if m.content.CardView.Fields["note"] != "한글" {
		t.Fatal("release inserted IME text")
	}
}

func TestDecisionOverlayOwnsKeysAboveExistingComposer(t *testing.T) {
	m := cardModel(t)
	m.content.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}, {Name: "waiting", Version: "1.0"}}
	m.content.CardView = state.CardView{}
	m.events = true
	m.content, _ = state.Reduce(m.content, state.EditDraft{Draft: state.Draft{Text: "unsent draft"}})
	m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
	if m.content.CardView.ID != "approval" || m.View().Cursor != nil {
		t.Fatal("card failed to own focus")
	}
	m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("composer swallowed card confirm")
	}
	if _, ok := cmd().(shellCardAnswer); !ok {
		t.Fatal("card entered conversation send path")
	}
	if m.content.Draft.Text != "unsent draft" {
		t.Fatal("card changed composer draft")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.content.CardView.ID != "" || m.content.Draft.Text != "unsent draft" {
		t.Fatal("closing card lost draft")
	}
}

func TestWaitingProjectionDropsResolvedDecisionsAndKeepsOtherRows(t *testing.T) {
	m := cardModel(t)
	card := m.content.Cards[0]
	m.content.Blocks = []state.Block{{ID: "decision", Topic: "waiting", Card: &card}, {ID: "other", Topic: "waiting", Text: "Other waiting row"}}
	rows := m.topicBlocks("waiting")
	if len(rows) != 2 || rows[0].Text == "" {
		t.Fatal("decision missing from waiting projection")
	}
	m.content.Cards[0].Outcome = state.CardResolved
	rows = m.topicBlocks("waiting")
	if len(rows) != 1 || rows[0].ID != "other" {
		t.Fatal("resolved decision still counted")
	}
}

func TestProductionCardNotificationReachesExistingRenderer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	recorder, err := term.NewRecorder(80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	m := newShell(Options{Width: 80, Notification: "bell"}, nil)
	m.focused = false
	d := &display{interactive: true, output: recorder, renderer: startTerminalRenderer(ctx, recorder, 80, 24)}
	defer d.renderer.close()
	if err := d.notifyCard(m, state.CardBell{Agent: "Agent", Summary: "Send"}); err != nil {
		t.Fatal(err)
	}
	if _, err := recorder.Wait(ctx, func(term.Snapshot) bool { return bytes.Count(recorder.Bytes(), []byte{'\a'}) == 1 }); err != nil {
		t.Fatal(err)
	}
	m.focused = true
	if err := d.notifyCard(m, state.CardBell{}); err != nil {
		t.Fatal(err)
	}
	d.renderer.close()
	if bytes.Count(recorder.Bytes(), []byte{'\a'}) != 1 {
		t.Fatal("focused card notified")
	}
}
