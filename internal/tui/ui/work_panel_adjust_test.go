// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// adjustShell focuses the panel over cards Core already projected, with the
// work capability granted as installed Core grants it.
func adjustShell(cards []state.WorkCard) *shellModel {
	m := navigationShell(120, cards)
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "work", Version: "1.0"})
	m.View()
	return m
}

// goalCards are goal lines: Core names each card's goal apart from its ref.
func goalCards(n int) []state.WorkCard {
	cards := navigationCards(n)
	for i := range cards {
		cards[i].GoalID = "goal-" + cards[i].ID
	}
	return cards
}

func adjustCommand(t *testing.T, m *shellModel, msg tea.KeyMsg) protocol.Payload {
	t.Helper()
	_, cmd := m.Update(msg)
	if cmd == nil {
		return nil
	}
	management, ok := cmd().(shellManagement)
	if !ok {
		t.Fatalf("key produced %T, not a management request", cmd())
	}
	return management.Request
}

func TestWorkPanelAdjustSendsThePersonsChoice(t *testing.T) {
	for name, tc := range map[string]struct {
		msg       tea.KeyMsg
		operation string
		confirm   bool
	}{
		"move up":   {tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}, "rank_up", false},
		"move down": {tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}, "rank_down", false},
		"pause":     {tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}, "pause", false},
		"stop":      {tea.KeyPressMsg{Code: tea.KeyDelete}, "stop", true},
	} {
		t.Run(name, func(t *testing.T) {
			m := adjustShell(goalCards(4))
			goal := "goal-" + m.conversation.workSelected
			if tc.operation == "stop" {
				// The stop asks first and sends only on Enter.
				if request := adjustCommand(t, m, tc.msg); request != nil {
					t.Fatalf("Delete sent %T before the person confirmed", request)
				}
				request, ok := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeyEnter}).(protocol.WorkControlRequest)
				if !ok || request.GoalID != goal || request.Confirm == nil || !*request.Confirm {
					t.Fatalf("Enter did not confirm the stop: %+v", request)
				}
				return
			}
			request, ok := adjustCommand(t, m, tc.msg).(protocol.WorkControlRequest)
			if !ok {
				t.Fatalf("%s sent no control request", name)
			}
			if request.Operation != tc.operation || request.GoalID != goal || request.Confirm != nil {
				t.Fatalf("%s sent %+v", name, request)
			}
		})
	}
}

func TestWorkPanelStopAsksThenCancels(t *testing.T) {
	m := adjustShell(goalCards(4))
	selected := m.conversation.workSelected
	adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	rows := m.workPanelRows(40, 20)
	ask := design.Text(design.English, "work.stop_confirm")
	if !strings.Contains(strings.Join(rows, "\n"), ask) {
		t.Fatalf("the card does not ask for confirmation: %q", rows)
	}
	// Any other key cancels and does nothing else: Enter must not open the 일.
	request := adjustCommand(t, m, tea.KeyPressMsg{Code: 'x', Text: "x"})
	if request != nil || m.conversation.workStop != "" || m.conversation.workStages.card != "" {
		t.Fatalf("a cancel key acted: request=%T stop=%q card=%q", request, m.conversation.workStop, m.conversation.workStages.card)
	}
	if rows := m.workPanelRows(40, 20); strings.Contains(strings.Join(rows, "\n"), ask) {
		t.Fatal("the ask stayed after a cancel key")
	}
	if m.conversation.workSelected != selected {
		t.Fatal("the ask moved the selection")
	}
}

func TestWorkPanelPausedCardResumes(t *testing.T) {
	cards := goalCards(4)
	cards[0].Paused = true
	cards[0].Now = design.Text(design.English, "progress.paused")
	m := adjustShell(cards)
	request, ok := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(protocol.WorkControlRequest)
	if !ok || request.Operation != "resume" {
		t.Fatalf("Space on a paused card sent %+v", request)
	}
}

func TestWorkPanelLimitKeysAndTitle(t *testing.T) {
	for name, tc := range map[string]struct {
		msg       tea.KeyMsg
		operation string
	}{
		"down": {tea.KeyPressMsg{Code: '-', Text: "-"}, "limit_down"},
		"up":   {tea.KeyPressMsg{Code: '=', Text: "+", Mod: tea.ModShift}, "limit_up"},
		"auto": {tea.KeyPressMsg{Code: '0', Text: "0"}, "limit_auto"},
	} {
		t.Run(name, func(t *testing.T) {
			m := adjustShell(navigationCards(4))
			request, ok := adjustCommand(t, m, tc.msg).(protocol.WorkLimitRequest)
			if !ok || request.Operation != tc.operation || request.Value != nil {
				t.Fatalf("%s sent %+v", name, request)
			}
		})
	}
	m := adjustShell(navigationCards(4))
	if rows := m.workPanelRows(40, 20); strings.Contains(rows[0], design.Text(design.English, "work.limit")) {
		t.Fatalf("automatic shows a limit: %q", rows[0])
	}
	m.content.Blocks = []state.Block{{Topic: "progress", Work: &state.WorkSummary{Cards: navigationCards(4), Limit: &state.WorkLimit{Value: 3}}}}
	if rows := m.workPanelRows(40, 20); !strings.Contains(rows[0], "3") {
		t.Fatalf("a set limit is not in the title: %q", rows[0])
	}
}

// Without Core's grant the panel sends nothing: Core decides, and the TUI never
// assumes the capability it did not negotiate.
func TestWorkPanelAdjustNeedsTheWorkCapability(t *testing.T) {
	m := navigationShell(120, navigationCards(4))
	for _, msg := range []tea.KeyPressMsg{
		{Code: tea.KeyUp, Mod: tea.ModShift},
		{Code: tea.KeySpace, Text: " "},
		{Code: '-', Text: "-"},
	} {
		if request := adjustCommand(t, m, msg); request != nil {
			t.Fatalf("ungranted panel sent %T", request)
		}
	}
}

// A held key repeats on terminals that report key events. Pause and stop are
// toggles, so a repeat can neither undo the pause Core just applied nor cancel
// the stop ask the first press opened.
func TestWorkPanelToggleKeysIgnoreRepeats(t *testing.T) {
	cards := goalCards(4)
	m := adjustShell(cards)
	if request, ok := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(protocol.WorkControlRequest); !ok || request.Operation != "pause" {
		t.Fatalf("Space sent %+v", request)
	}
	cards[0].Paused = true
	m.content.Blocks = []state.Block{{Topic: "progress", Work: &state.WorkSummary{Cards: cards}}}
	if request := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " ", IsRepeat: true}); request != nil {
		t.Fatalf("a held Space sent %+v after Core's pause", request)
	}
	adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeyDelete})
	adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeyDelete, IsRepeat: true})
	if m.conversation.workStop == "" {
		t.Fatal("a held Delete cancelled its own stop ask")
	}
	// Moving and the limit keys still repeat.
	m.conversation.workStop = ""
	if request := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift, IsRepeat: true}); request == nil {
		t.Fatal("a held Shift+Down did not move the card")
	}
}

// Only a goal line accepts goal commands. A background run or a conversation
// worker has a ref of its own, which is not a goal.
func TestWorkPanelGoalControlsNeedAGoal(t *testing.T) {
	cards := goalCards(2)
	cards[0].GoalID = ""
	m := adjustShell(cards)
	// Wide enough for every work hint, so a missing hint is the panel's choice.
	m.Update(tea.WindowSizeMsg{Width: 300, Height: 34})
	for _, msg := range []tea.KeyPressMsg{
		{Code: tea.KeyUp, Mod: tea.ModShift},
		{Code: tea.KeyDown, Mod: tea.ModShift},
		{Code: tea.KeySpace, Text: " "},
		{Code: tea.KeyDelete},
	} {
		if request := adjustCommand(t, m, msg); request != nil {
			t.Fatalf("%s on a card without a goal sent %+v", msg, request)
		}
	}
	if m.conversation.workStop != "" {
		t.Fatal("Delete asked to stop a card without a goal")
	}
	footer := ansi.Strip(m.conversationFooter(false))
	for _, hint := range []string{"work.move", "work.pause", "work.stop"} {
		if strings.Contains(footer, design.Text(design.English, hint)) {
			t.Fatalf("footer offers %s for a card without a goal: %q", hint, footer)
		}
	}
	if request, ok := adjustCommand(t, m, tea.KeyPressMsg{Code: '-', Text: "-"}).(protocol.WorkLimitRequest); !ok || request.Operation != "limit_down" {
		t.Fatalf("the run limit is not per card, got %+v", request)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if request, ok := adjustCommand(t, m, tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}).(protocol.WorkControlRequest); !ok || request.GoalID != "goal-1" {
		t.Fatalf("Space on a goal line sent %+v", request)
	}
	footer = ansi.Strip(m.conversationFooter(false))
	if !strings.Contains(footer, design.Text(design.English, "work.pause")) {
		t.Fatalf("footer hides pause on a goal line: %q", footer)
	}
}

func TestProjectWorkKeepsTheGoalApartFromTheRef(t *testing.T) {
	state := protocol.RunningStateInProgress
	goal := "goal-a"
	line := protocol.ProgressLine{Name: "Work", Kind: "project", Stage: "unknown", Next: "unknown", JustFinished: "unknown", Waiting: "0", Depth: "0", State: &state}
	rows := []protocol.ProgressRow{{Ref: "goal-a", GoalID: &goal, Line: line}, {Ref: "run:b", Line: line}, {Ref: "session:c", Line: line}}
	summary := projectWork(protocol.ProgressUpdate{Rows: &rows})
	if len(summary.Cards) != 3 || summary.Cards[0].GoalID != "goal-a" || summary.Cards[1].GoalID != "" || summary.Cards[2].GoalID != "" {
		t.Fatalf("cards = %+v", summary.Cards)
	}
}

// The adjust keys never change the card locally; Core's update does.
func TestWorkPanelAdjustKeepsTheCardUntilCoreUpdates(t *testing.T) {
	cards := goalCards(4)
	m := adjustShell(cards)
	before := ansi.Strip(m.View().Content)
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift})
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
	m.Update(tea.KeyPressMsg{Code: '-', Text: "-"})
	if after := ansi.Strip(m.View().Content); after != before {
		t.Fatalf("a choice changed the panel before Core reported it:\n%s\n---\n%s", before, after)
	}
	if m.content.Draft.Text != "keep 초안" || !m.conversation.workFocused {
		t.Fatal("a choice changed the draft or the focus")
	}
}
