// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

// countFrame carries Core's count beside the rows of one progress update.
func countFrame(t *testing.T, rows []protocol.ProgressRow, counts string) state.Frame {
	t.Helper()
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	var e protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &e); err != nil {
		t.Fatal(err)
	}
	e.SentAt = "2026-10-23T12:42:00Z"
	e.Payload = protocol.Snapshot{Topic: "progress", Cursor: protocol.StreamCursor{Epoch: e.Epoch, Seq: json.Number("1")}, RevocationEpoch: "0", Items: []protocol.StreamItem{{Kind: protocol.KindProgressUpdate, Key: "overview", Revision: json.Number("1"), Payload: json.RawMessage(`{"counts":` + counts + `,"rows":` + string(raw) + `}`)}}}
	return streamFrame(t, e)
}

// countRows is the scenario's shape: three 일 that hold twelve tasks and one
// background run outside any 일. The tasks are rows of their 일, marked row, so
// they are never cards of their own.
func countRows() []protocol.ProgressRow {
	rows := []protocol.ProgressRow{}
	line := func(ref, name, kind, depth string, state protocol.RunningState, waiting string, row bool) protocol.ProgressRow {
		value := state
		out := protocol.ProgressRow{Ref: ref, Line: protocol.ProgressLine{Name: name, Kind: kind, Stage: "unknown", JustFinished: "unknown", Next: "unknown", Waiting: json.Number(waiting), Depth: json.Number(depth), State: &value}}
		if row {
			inner := true
			out.Line.Row = &inner
		}
		return out
	}
	for i := range 3 {
		day := fmt.Sprintf("day-%d", i)
		rows = append(rows, line(day, fmt.Sprintf("Day %d", i), "project", "0", protocol.RunningStateInProgress, "0", false))
		for j := range 4 {
			rows = append(rows, line(fmt.Sprintf("%s-step-%d", day, j), fmt.Sprintf("Task %d", j), "project", "1", protocol.RunningStateInProgress, "0", true))
		}
	}
	return append(rows, line("run:survey", "Read competitor notes", "project", "0", protocol.RunningStateNeedsInput, "1", false))
}

// TestStatusCountDrawsCoreLines checks that the status line shows Core's count of
// lines — four by state — while the same frame carries thirteen cards.
func TestStatusCountDrawsCoreLines(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running", "progress", "waiting")})
	rows := countRows()
	if len(rows) != 16 {
		t.Fatalf("fixture rows = %d", len(rows))
	}
	m.Update(shellData{Generation: 1, Message: countFrame(t, rows, `{"running":3,"waiting":1}`)})
	cards := m.workCards()
	if len(cards) != 4 {
		t.Fatalf("work cards = %d, want the three 일 and the run outside any 일: %+v", len(cards), cards)
	}
	for _, card := range cards {
		if strings.HasPrefix(card.Name, "Task ") {
			t.Fatalf("a row of a 일 became a card: %+v", card)
		}
	}
	footer := ansi.Strip(m.conversationFooter(false))
	if want := fmt.Sprintf(design.Text(design.English, "work.count"), 3, 1); !strings.Contains(footer, want) {
		t.Fatalf("status count = %q, want %q", footer, want)
	}
}

// TestStatusCountFallsBackForOlderCore checks that a Core which sends no count
// still gets one from the cards it sends.
func TestStatusCountFallsBackForOlderCore(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "running", "progress", "waiting")})
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 1, `[{"ref":"job","line":{"name":"Login repair","kind":"project","stage":"unknown","just_finished":"unknown","next":"unknown","waiting":0,"depth":0,"state":"in_progress"}}]`)})
	footer := ansi.Strip(m.conversationFooter(false))
	if want := fmt.Sprintf(design.Text(design.English, "work.count"), 1, 0); !strings.Contains(footer, want) {
		t.Fatalf("status count = %q, want %q", footer, want)
	}
}
