// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	"github.com/thisisjun786/lina/internal/tui/state"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func viewCards(n int, at time.Time) []state.WorkCard {
	cards := navigationCards(n)
	states := []protocol.RunningState{protocol.RunningStateNeedsInput, protocol.RunningStateInProgress, protocol.RunningStateWaiting, protocol.RunningStateDone}
	for i := range cards {
		cards[i].State = states[i%4]
		cards[i].Waiting = 0
		if i%4 == 0 {
			cards[i].Waiting = 1
		}
		cards[i].Started = at.Add(-42 * time.Minute)
		if i%4 == 3 {
			cards[i].Finished = at
		}
	}
	return cards
}

func TestWorkPanelViewsMatrix(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{120, 100} {
				for _, n := range []int{20, 50} {
					t.Run(fmt.Sprintf("%s/ascii=%t/%d/N=%d", language, ascii, width, n), func(t *testing.T) {
						at := time.Date(2026, 10, 23, 12, 42, 0, 0, time.UTC)
						cards := viewCards(n, at)
						m := navigationShell(width, cards)
						m.now = func() time.Time { return at }
						m.styles = NewStyles(Options{Profile: TrueColor, Language: language, ASCII: ascii})
						anchor := m.conversation.viewport.anchor
						plan := m.conversationPlan()
						w, h := plan.SidePane.Width, plan.SidePane.Height
						if width < 120 {
							w, h = plan.Conversation.Width, plan.Conversation.Height
						}
						// First Ctrl+P focused the cards; the second must compact them.
						m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
						rows := m.workPanelRows(w, h)
						if !strings.Contains(rows[1], cards[0].Name) || !strings.Contains(rows[1], cards[0].Now) || !strings.HasSuffix(rows[1], "42m") {
							t.Fatalf("E10 compact must put name, Now and elapsed in one row: %q", rows)
						}
						for _, compact := range []bool{true, false} {
							if !compact {
								m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
							}
							for filter := 0; filter < 5; filter++ {
								rows = m.workPanelRows(w, h)
								shown, folded, total := 0, 0, 0
								text := strings.Join(rows, "\n")
								for i, c := range cards {
									matches := filter == 0 || i%4 == filter-1
									if matches {
										total++
										if i%4 == 3 {
											folded++
										}
									}
									present := strings.Contains(text, c.Name)
									if present && (!matches || i%4 == 3) {
										t.Fatalf("filter %d leaked %s: %q", filter, c.Name, rows)
									}
									if present {
										shown++
										for j, row := range rows {
											if !strings.Contains(row, c.Name) {
												continue
											}
											activity := j + 1
											if compact {
												activity = j
											}
											if activity >= len(rows) || !strings.Contains(rows[activity], c.Now) || !strings.HasSuffix(row, "42m") {
												t.Fatalf("card split/lost fields: %q", rows)
											}
										}
									}
								}
								if len(rows) > h {
									t.Fatalf("height overflow: %q", rows)
								}
								for _, row := range rows {
									if (render.Unicode{}).Width(row) > w {
										t.Fatalf("width overflow: %q", row)
									}
								}
								if total > folded && shown == 0 {
									t.Fatalf("filter %d hid all cards: %q", filter, rows)
								}
								done := fmt.Sprintf(design.Text(language, "work.done"), folded)
								if folded > 0 && !strings.Contains(text, done) {
									t.Fatalf("fold count missing %q: %q", done, rows)
								}
								tail := "…"
								if ascii {
									tail = "..."
								}
								if hidden := total - folded - shown; hidden > 0 {
									want := fmt.Sprintf(design.Text(language, "work.more"), tail, hidden)
									if !strings.Contains(rows[len(rows)-1], want) {
										t.Fatalf("overflow count missing %q: %q", want, rows)
									}
								}
								if filter > 0 && rows[0] == design.Text(language, "work.title") {
									t.Fatalf("filter absent from title: %q", rows)
								}
								if m.content.Draft.Text != "keep 초안" || !m.conversation.workFocused || m.conversation.viewport.anchor != anchor {
									t.Fatal("view changed draft/focus/anchor")
								}
								m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
							}
						}
					})
				}
			}
		}
	}
}

func TestWorkPanelViewsSelectionAndUpdates(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 42, 0, 0, time.UTC)
	cards := viewCards(50, at)
	m := navigationShell(120, cards)
	m.now = func() time.Time { return at }
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	// Select a needs-you card; retain it in that filter, then fall back to
	// the same position when it is excluded by the running filter.
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	if m.conversation.workSelected != cards[4].ID {
		t.Fatal("filter lost matching selection")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	active, done := m.selectableWorkCards()
	if len(active) != 13 || done != 0 || m.conversation.workSelected != cards[5].ID {
		t.Fatalf("filtered selection: %s, %d active/%d done", m.conversation.workSelected, len(active), done)
	}
	for i := 0; i < 20; i++ {
		m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	assertSelectedWorkVisible(t, m, cards[49])
	// A live change removes the selection from this filter, selecting its neighbor.
	cards[49].State = protocol.RunningStateWaiting
	assertSelectedWorkVisible(t, m, cards[45])
	for i := range cards {
		cards[i].State = protocol.RunningStateDone
		cards[i].Waiting = 0
	}
	if rows := m.workPanelRows(40, 29); len(rows) != 1 || m.conversation.workSelected != "" {
		t.Fatalf("empty filter retained content: %q", rows)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft}) // wrap all to done
	if rows := m.workPanelRows(40, 29); len(rows) != 2 || rows[1] != fmt.Sprintf(design.Text(m.styles.options.Language, "work.done"), 50) {
		t.Fatalf("left wrap/done count: %q", rows)
	}
}

func TestWorkPanelViewsGraphemeBudgets(t *testing.T) {
	for _, ascii := range []bool{false, true} {
		for _, name := range []string{"Short", strings.Repeat("é", 60), strings.Repeat("👩‍💻", 30), strings.Repeat("자료", 30)} {
			m := navigationShell(120, []state.WorkCard{{ID: "a", Name: name, Now: strings.Repeat("확인", 30)}})
			m.styles = NewStyles(Options{Language: design.Korean, ASCII: ascii})
			m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
			rows := m.workPanelRows(40, 29)
			if len(rows) != 2 || (render.Unicode{}).Width(rows[1]) != 40 || !strings.Contains(rows[1], "확인") || !strings.HasSuffix(rows[1], design.Text(design.Korean, "unknown")) {
				t.Fatalf("compact clipping lost fields: %q", rows)
			}
			if name == "Short" && !strings.Contains(rows[1], name) {
				t.Fatalf("Now displaced name: %q", rows)
			}
		}
	}
}

func TestWorkPanelViewsObservedStatesAndFilteredOverflow(t *testing.T) {
	cards := navigationCards(20)
	for i := range cards {
		cards[i].Waiting = 1
		cards[i].State = protocol.RunningStateDone
	}
	// Core's person-wait count takes precedence over the execution state.
	cards = append(cards,
		state.WorkCard{ID: "unknown", Name: "Unknown", State: protocol.RunningStateUnknown},
		state.WorkCard{ID: "failed", Name: "Failed", State: protocol.RunningStateFailed},
		state.WorkCard{ID: "review", Name: "Review", State: protocol.RunningStateReadyForReview},
		state.WorkCard{ID: "stopped", Name: "Stopped", State: protocol.RunningStateStopped},
		state.WorkCard{ID: "needs", Name: "Needs", State: protocol.RunningStateNeedsInput},
	)
	m := navigationShell(120, cards)
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
	active, done := m.selectableWorkCards()
	if len(active) != 21 || done != 0 {
		t.Fatalf("person requests hidden: %d/%d", len(active), done)
	}
	rows := m.workPanelRows(40, 10)
	// One title, eight compact cards, one overflow: thirteen hidden person
	// requests. No excluded or folded cards inflate totals.
	tail := rows[len(rows)-1]
	for _, want := range []string{fmt.Sprintf(design.Text(m.styles.options.Language, "work.more"), "…", 13), fmt.Sprintf(design.Text(m.styles.options.Language, "work.needs"), 13)} {
		if !strings.Contains(tail, want) {
			t.Fatalf("filtered overflow missing %q: %q", want, rows)
		}
	}
	for _, filter := range []protocol.RunningState{protocol.RunningStateInProgress, protocol.RunningStateWaiting, protocol.RunningStateDone} {
		m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
		if rows := m.workPanelRows(40, 10); len(rows) != 1 {
			t.Fatalf("filter %s relabeled unobserved states or counted excluded cards: %q", filter, rows)
		}
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	if rows := m.workPanelRows(40, 10); len(rows) != 1 {
		t.Fatalf("reopening lost filter: %q", rows)
	}
}

func TestWorkPanelViewsBesideWideWaitingText(t *testing.T) {
	for _, label := range []string{"👩‍💻", strings.Repeat("👩‍💻", 30)} {
		m := navigationShell(120, navigationCards(50))
		m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "waiting", Version: "1.0"})
		m.content.Blocks = append(m.content.Blocks, state.Block{ID: "waiting", Topic: "waiting", Text: label})
		m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		view := ansi.Strip(m.conversationContent(false))
		plan := m.conversationPlan()
		row := strings.Split(view, "\n")[plan.Cards.Y]
		right := m.workPanelRows(plan.SidePane.Width, plan.SidePane.Height)[plan.Cards.Y-plan.SidePane.Y]
		start := strings.LastIndex(row, right)
		if start < 0 || (render.Unicode{}).Width(row[:start]) != plan.SidePane.X {
			t.Fatalf("left text shifted/clipped work panel: want column %d row=%q", plan.SidePane.X, row)
		}
		prefix := render.Truncate(row, plan.SidePane.X, render.Unicode{})
		if (render.Unicode{}).Width(prefix) != plan.SidePane.X || !strings.HasSuffix(prefix, " ") {
			t.Fatalf("missing sidebar gap: %q", prefix)
		}
	}
}

func TestWorkPanelViewsTerminalColumns(t *testing.T) {
	for _, label := range []string{"👩‍💻", strings.Repeat("👩‍💻", 30)} {
		m := navigationShell(120, navigationCards(50))
		m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "waiting", Version: "1.0"})
		m.content.Blocks = append(m.content.Blocks, state.Block{ID: "waiting", Topic: "waiting", Text: label})
		m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		t.Cleanup(cancel)
		output, err := tuiterm.NewRecorder(120, 34)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { output.Close() })
		renderer := startTerminalRenderer(ctx, rawTerminalWriter{output}, 120, 34, TrueColor)
		t.Cleanup(func() { renderer.close() })
		if err = renderer.showFlushed(m.View(), tea.WindowSizeMsg{Width: 120, Height: 34}); err != nil {
			t.Fatal(err)
		}
		plan := m.conversationPlan()
		right := m.workPanelRows(plan.SidePane.Width, plan.SidePane.Height)[plan.Cards.Y-plan.SidePane.Y]
		var actual strings.Builder
		for _, cell := range output.Snapshot().Cells[plan.Cards.Y][plan.SidePane.X:] {
			actual.WriteString(cell.Content)
		}
		before := output.Snapshot().Writes
		if err = renderer.showFlushed(m.View(), tea.WindowSizeMsg{Width: 120, Height: 34}); err != nil {
			t.Fatal(err)
		}
		if output.Snapshot().Writes != before {
			t.Fatal("unchanged panel repainted")
		}
		m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		view := m.View()
		if err = renderer.showFlushed(view, tea.WindowSizeMsg{Width: 120, Height: 34}); err != nil {
			t.Fatal(err)
		}
		snapshot := output.Snapshot()
		if view.Cursor == nil || snapshot.CursorHidden || snapshot.CursorX != view.Cursor.X || snapshot.CursorY != view.Cursor.Y {
			t.Fatal("column repair moved the composer cursor")
		}
		renderer.close()
		output.Close()
		cancel()
		if strings.TrimRight(actual.String(), " ") != right {
			t.Fatalf("terminal lost panel columns beside %q: got %q, want %q", label, actual.String(), right)
		}
	}
}
