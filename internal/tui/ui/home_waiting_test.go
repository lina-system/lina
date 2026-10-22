// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func homeWaitingFrame(t *testing.T, seq string, snapshot bool, names []string, more string) state.Frame {
	t.Helper()
	var e protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &e); err != nil {
		t.Fatal(err)
	}
	waiting := []protocol.WaitingRow{}
	for i, name := range names {
		waiting = append(waiting, protocol.WaitingRow{
			Ref:  protocol.WaitingRef{Source: "exchange", Key: fmt.Sprint("hidden-", i), Revision: "1"},
			Line: protocol.WaitingLine{Kind: "reply", Sentence: name, AgentName: "Lina", Answers: []string{}, Effort: "0"},
		})
	}
	count := json.Number(more)
	p := protocol.ProgressUpdate{Waiting: &waiting, WaitingMore: &count}
	cursor := protocol.StreamCursor{Epoch: e.Epoch, Seq: json.Number(seq)}
	if snapshot {
		raw, _ := json.Marshal(p)
		e.Payload = protocol.Snapshot{Topic: "progress", Cursor: cursor, RevocationEpoch: "0", Items: []protocol.StreamItem{{Kind: protocol.KindProgressUpdate, Key: "overview", Revision: json.Number(seq), Payload: raw}}}
	} else {
		e.Kind = protocol.KindProgressUpdate
		p.Stream = &protocol.StreamMetadata{Topic: "progress", Key: "overview", Cursor: cursor, Revision: json.Number(seq)}
		e.Payload = p
	}
	return streamFrame(t, e)
}

func TestHomeWaitingKeepsDecisionForms(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "waiting", "progress")})
	m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
	m.Update(tea.PasteMsg{Content: "KEEP-DRAFT"})
	card := state.Card{ID: "approval", Epoch: "42", Revision: 1, Token: "authenticated-revision", Kind: protocol.KindApprovalRequest, Agent: "Lina", Summary: "Send the document", Choices: []protocol.ApprovalChoice{protocol.ApprovalChoiceOnce, protocol.ApprovalChoiceDeny}}
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "0", Topic: "waiting", Cursor: 1, Items: []state.Block{{ID: "approval", Card: &card, Done: true, Revision: 1}}}})
	if m.conversationPlan().CardCount != 1 {
		t.Fatal("decision fallback missing before overview")
	}
	m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "1", true, []string{"Send the document", "Resolve the record change"}, "0")})
	plan := m.conversationPlan()
	rows := strings.Split(m.View().Content, "\n")
	home := strings.Join(rows[plan.Cards.Y:plan.Cards.Y+plan.Cards.Height], "\n")
	if plan.CardCount != 2 || strings.Count(home, "Send the document") != 1 || !strings.Contains(home, "Resolve the record change") {
		t.Fatalf("overview duplicated or omitted Home rows: %s", home)
	}
	m.cardKey(term.Key{Name: "f6", Events: true})
	opened, ok := m.content.CurrentCard()
	if !ok || opened.ID != card.ID || opened.Token != card.Token {
		t.Fatal("overview replaced the authenticated decision")
	}
	m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "2", false, nil, "0")})
	if m.conversationPlan().Cards.Height != 0 || m.content.CardView.ID != card.ID || m.content.Draft.Text != "KEEP-DRAFT" {
		t.Fatal("overview clear kept stale Home cards or changed the form/draft")
	}
	m.cardKey(term.Key{Name: "1", Events: true})
	command, _ := m.cardKey(term.Key{Name: "enter", Events: true})
	if command == nil {
		t.Fatal("approval no longer uses the existing confirmation flow")
	}
	answer := command().(shellCardAnswer).Request
	if answer.ID != card.ID || answer.Token != card.Token || answer.Revision != card.Revision || answer.Choice != protocol.ApprovalChoiceOnce {
		t.Fatalf("approval routing changed: %+v", answer)
	}
}

func TestHomeWaitingOverview(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		t.Run(string(language), func(t *testing.T) {
			m := newShell(Options{Width: 120, Profile: Monochrome, Language: language}, time.Now)
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "waiting", "progress", "running")})
			m.Update(tea.PasteMsg{Content: "KEEP-DRAFT"})
			names := []string{"Choose the destination", "Confirm the deadline", "Resolve the record change", "Supply the missing input", "Choose the format"}
			m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "1", true, names, "2")})
			view := m.View().Content
			previous := -1
			for _, name := range names {
				at := strings.Index(view, name)
				if at <= previous || strings.Count(view, name) != 1 {
					t.Fatalf("Home lost Core waiting order or duplicated a row: %s", view)
				}
				previous = at
			}
			more := fmt.Sprintf(design.Text(language, "progress.more"), "2")
			if !strings.Contains(view, more) || m.conversationPlan().CardCount != 7 || strings.Contains(view, "hidden-") {
				t.Fatalf("Home lost total/remaining count or exposed identity: %s", view)
			}
			m.Update(tea.WindowSizeMsg{Width: 50, Height: 10})
			view = m.View().Content
			if m.conversationPlan().CardCount != 7 || !strings.Contains(view, fmt.Sprintf(design.Text(language, "progress.more"), "6")) {
				t.Fatalf("folded Home lost hidden rows: %s", view)
			}
			m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
			m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "2", false, []string{"Updated request"}, "0")})
			m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "1", true, names, "2")})
			view = m.View().Content
			if !strings.Contains(view, "Updated request") || strings.Contains(view, names[0]) || m.conversationPlan().CardCount != 1 {
				t.Fatalf("live update or stale snapshot handling failed: %s", view)
			}
			m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "3", true, nil, "0")})
			if m.conversationPlan().Cards.Height != 0 || strings.Contains(m.View().Content, "Updated request") || m.content.Draft.Text != "KEEP-DRAFT" || m.conversation.pager.open {
				t.Fatal("empty overview retained rows or changed draft/focus")
			}
		})
	}
}

func TestHomeWaitingResyncAndCapability(t *testing.T) {
	m := conversationShell()
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "waiting", "progress")})
	m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "1", true, []string{"Current request"}, "0")})
	m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "3", false, []string{"Unaccepted request"}, "9")})
	if !strings.Contains(m.View().Content, "Current request") || strings.Contains(m.View().Content, "Unaccepted request") || m.conversationPlan().CardCount != 1 {
		t.Fatal("gap bypassed the reducer's accepted overview")
	}
	m.Update(shellData{Generation: 1, Message: homeWaitingFrame(t, "3", true, []string{"Resynced request"}, "2")})
	if !strings.Contains(m.View().Content, "Resynced request") || m.conversationPlan().CardCount != 3 {
		t.Fatal("replacement snapshot did not replace the summary")
	}
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "waiting")})
	if m.conversationPlan().Cards.Height != 0 || strings.Contains(m.View().Content, "Resynced request") {
		t.Fatal("unnegotiated overview remained visible")
	}
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "waiting", "progress")})
	m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: "42", Revocation: "0", Topic: "progress", Cursor: 4}})
	if m.conversationPlan().Cards.Height != 0 || strings.Contains(m.View().Content, "Resynced request") {
		t.Fatal("cleared progress topic retained its summary")
	}
}
