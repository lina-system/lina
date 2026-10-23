// SPDX-License-Identifier: Apache-2.0
package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/thisisjun786/lina/internal/tui/state"
	"github.com/thisisjun786/lina/kit/protocol"
)

func workPanelFrame(t *testing.T, seq int, rows string) state.Frame {
	t.Helper()
	var e protocol.Envelope
	if err := json.Unmarshal(streamFixture(t, "normal/snapshot"), &e); err != nil {
		t.Fatal(err)
	}
	e.SentAt = "2026-10-23T12:42:00Z"
	e.Payload = protocol.Snapshot{Topic: "progress", Cursor: protocol.StreamCursor{Epoch: e.Epoch, Seq: json.Number(strconv.Itoa(seq))}, RevocationEpoch: "0", Items: []protocol.StreamItem{{Kind: protocol.KindProgressUpdate, Key: "overview", Revision: json.Number(strconv.Itoa(seq)), Payload: json.RawMessage(`{"rows":` + rows + `}`)}}}
	return streamFrame(t, e)
}

func TestWorkPanelVisibleWithCoreTimeAndActivity(t *testing.T) {
	m := conversationShell()
	m.now = func() time.Time { return time.Date(2026, 10, 23, 12, 42, 0, 0, time.UTC) }
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "progress")})
	rows := `[{"ref":"job","line":{"name":"Login repair","kind":"project","stage":"unknown","just_finished":"unknown","next":"unknown","waiting":0,"depth":0,"state":"in_progress","started_at":"2026-10-23T12:00:00Z","now":"Updating login"}}]`
	m.Update(shellData{Generation: 1, Message: workPanelFrame(t, 1, rows)})
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Login repair") || !strings.Contains(view, "42m") || !strings.Contains(view, "Updating login") {
		t.Fatalf("missing automatic work card:\n%s", view)
	}
	plan := m.conversationPlan()
	if plan.SidePane.Width != 40 || plan.Conversation.Width != 79 {
		t.Fatalf("panel/gap widths: %+v", plan)
	}
}

// E1-E5/E9: fixed clocks and stream revisions make ordering, overflow and calm
// updates observable without sleeps or assertions about catalog prose.
func TestWorkPanelMatrix(t *testing.T) {
	for _, language := range []design.Language{design.English, design.Korean} {
		for _, ascii := range []bool{false, true} {
			for _, width := range []int{120, 100} {
				for _, n := range []int{1, 5, 20, 50} {
					t.Run(fmt.Sprintf("%s/ascii=%t/%dx34/N=%d", language, ascii, width, n), func(t *testing.T) {
						at := time.Date(2026, 10, 23, 12, 42, 0, 0, time.UTC)
						m := newShell(Options{Width: width, Profile: TrueColor, Language: language, ASCII: ascii}, func() time.Time { return at })
						m.conversation.id = "main:fixture"
						m.Update(tea.WindowSizeMsg{Width: width, Height: 34})
						m.Update(shellConnection{Generation: 1, Status: ConnectionStatus{State: Live}})
						m.Update(shellData{Generation: 1, Message: workListWelcome(t, "conversation", "progress")})
						history := []state.Block{}
						for i := 0; i < 70; i++ {
							history = append(history, state.Block{ID: fmt.Sprint(i), Text: fmt.Sprintf("history %02d", i), Done: true, Revision: 1, Speaker: protocol.ConversationSpeakerAgent})
						}
						m.Update(shellData{Generation: 1, Message: state.Snapshot{Epoch: m.content.Epoch, Revocation: "0", Topic: "conversation:main:fixture", Cursor: 1, Items: history}})
						m.Update(tea.PasteMsg{Content: "keep 초안"})
						rows := make([]protocol.ProgressRow, 0, n+3)
						names := []string{"Needs reply", "로그인 수정", strings.Repeat("é", 60), strings.Repeat("👩‍💻", 20), strings.Repeat("資料", 30)}
						for i := 0; i < n; i++ {
							start := at.Add(-time.Duration(42+i*65) * time.Minute).Format(time.RFC3339Nano)
							now := fmt.Sprintf("step-%02d-A", i)
							status := protocol.RunningStateInProgress
							waiting := json.Number("0")
							if i == 0 {
								status = protocol.RunningStateNeedsInput
								waiting = "1"
							}
							rows = append(rows, protocol.ProgressRow{Ref: fmt.Sprint(i), Line: protocol.ProgressLine{Name: names[i%len(names)], Kind: "project", Stage: "unknown", Next: "unknown", JustFinished: "unknown", Waiting: waiting, Depth: "0", State: &status, StartedAt: &start, Now: &now}})
						}
						for i := 0; i < 3; i++ {
							status := protocol.RunningStateDone
							rows = append(rows, protocol.ProgressRow{Ref: fmt.Sprint("done", i), Line: protocol.ProgressLine{Name: "Finished", Kind: "project", Stage: "unknown", Next: "unknown", JustFinished: "unknown", Waiting: "0", Depth: "0", State: &status}})
						}
						apply := func(seq int) {
							raw, _ := json.Marshal(rows)
							m.Update(shellData{Generation: 1, Message: workPanelFrame(t, seq, string(raw))})
						}
						apply(1)
						if width < 120 {
							m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
						}
						view := ansi.Strip(m.View().Content)
						if !strings.Contains(view, "Needs reply") || !strings.Contains(view, "42m") {
							t.Fatalf("E1/E3 card absent:\n%s", view)
						}
						panelWidth, panelHeight := m.conversationPlan().SidePane.Width, m.conversationPlan().SidePane.Height
						if width < 120 {
							panelWidth = m.conversationPlan().Conversation.Width
							panelHeight = m.conversationPlan().Conversation.Height
						}
						rendered := m.workPanelRows(panelWidth, panelHeight)
						shown := 0
						for i := 1; i+1 < len(rendered) && strings.HasPrefix(rendered[i+1], "  "); i += 2 {
							if !strings.HasSuffix(rendered[i], workElapsed(time.Duration(42+shown*65)*time.Minute)) || !strings.Contains(rendered[i+1], fmt.Sprintf("step-%02d-A", shown)) {
								t.Fatalf("E2/E3 wrong card: %q", rendered[i:i+2])
							}
							shown++
						}
						if shown != min(n, (panelHeight-2)/2) && n <= 5 {
							t.Fatalf("E2 too few cards: %d", shown)
						}
						for _, line := range rendered {
							if (render.Unicode{}).Width(line) > panelWidth {
								t.Fatalf("E2 wraps: %q", line)
							}
						}
						done := fmt.Sprintf(design.Text(language, "work.done"), 3)
						if !strings.Contains(strings.Join(rendered, "\n"), done) {
							t.Fatalf("E5 done count: %q", rendered)
						}
						if n > shown {
							tail := "…"
							if ascii {
								tail = "..."
							}
							want := fmt.Sprintf(design.Text(language, "work.more"), tail, n-shown)
							if rendered[len(rendered)-1] != want {
								t.Fatalf("E5 overflow: %q want %q", rendered, want)
							}
						}
						if !strings.Contains(view, fmt.Sprintf(design.Text(language, "work.count"), n-1, 1)) {
							t.Fatalf("E5 status: %s", view)
						}
						if width < 120 {
							m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
						}
						m.scrollConversation(20)
						m.View()
						anchor := m.conversation.viewport.anchor
						if width < 120 {
							m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
						}
						focus := m.conversation.workFocused
						var renderer *terminalRenderer
						var output *tuiterm.Recorder
						ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
						defer cancel()
						if n == 50 {
							var err error
							output, err = tuiterm.NewRecorder(width, 34)
							if err != nil {
								t.Fatal(err)
							}
							defer output.Close()
							renderer = startTerminalRenderer(ctx, rawTerminalWriter{output}, width, 34, TrueColor)
							defer renderer.close()
						}
						for i := 2; i <= 51; i++ {
							next := fmt.Sprintf("step-00-%02d", i)
							rows[0].Line.Now = &next
							apply(i)
							m.View()
							if renderer != nil {
								if err := renderer.show(m.View(), tea.WindowSizeMsg{Width: width, Height: 34}); err != nil {
									t.Fatal(err)
								}
								if _, err := output.Wait(ctx, func(s tuiterm.Snapshot) bool {
									for _, row := range s.Cells {
										var text strings.Builder
										for _, cell := range row {
											text.WriteString(cell.Content)
										}
										if strings.Contains(text.String(), next) {
											return true
										}
									}
									return false
								}); err != nil {
									t.Fatal("rendered activity missing", err)
								}
							}

							if m.content.Draft.Text != "keep 초안" || m.conversation.workFocused != focus || m.conversation.viewport.anchor != anchor {
								t.Fatal("E9 update moved draft, focus or reading anchor")
							}
							if got := m.workPanelRows(panelWidth, panelHeight)[2]; !strings.Contains(got, next) {
								t.Fatalf("E4 did not move: %q", got)
							}
						}
						before := m.workPanelRows(panelWidth, panelHeight)[2]
						at = at.Add(time.Minute)
						m.Update(activityTick(at))
						if m.workPanelRows(panelWidth, panelHeight)[2] != before {
							t.Fatal("E4 clock changed activity")
						}
					})
				}
			}
		}
	}
}

func TestWorkPanelUnknownFinishAndCapability(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "progress", Version: "1.0"})
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return at.Add(9 * time.Hour) }
	m.content.Blocks = []state.Block{{ID: "overview", Topic: "progress", Work: &state.WorkSummary{Cards: []state.WorkCard{{Name: "Unknown", Now: "Unobserved"}, {Name: "Stopped clock", Started: at, Finished: at.Add(125 * time.Minute)}}}}}
	rows := m.workPanelRows(40, 20)
	if !strings.HasSuffix(rows[1], design.Text(m.styles.options.Language, "unknown")) || !strings.HasSuffix(rows[3], "2h 05m") {
		t.Fatalf("unknown/finish time: %q", rows)
	}
	m.content.Capabilities = m.content.Capabilities[:1]
	if m.conversationPlan().SidePane.Width != 0 || strings.Contains(m.View().Content, "Stopped clock") {
		t.Fatal("ungranted progress leaked")
	}
}

func TestWorkPanelClockOffsetAndMinuteBoundary(t *testing.T) {
	m := conversationShell()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "progress", Version: "1.0"})
	local := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return local }
	m.content.Offset = 2 * time.Hour
	m.content.Blocks = []state.Block{{Topic: "progress", Work: &state.WorkSummary{Cards: []state.WorkCard{{Name: "Task", Now: "Step", Started: local.Add(2*time.Hour - 59*time.Second)}}}}}
	if rows := m.workPanelRows(40, 10); !strings.HasSuffix(rows[1], "0m") {
		t.Fatalf("clock offset: %q", rows)
	}
	if m.workPanelInterval() != time.Second {
		t.Fatal("minute boundary not scheduled")
	}
	local = local.Add(time.Second)
	if rows := m.workPanelRows(40, 10); !strings.HasSuffix(rows[1], "1m") {
		t.Fatalf("clock did not advance: %q", rows)
	}
	if m.workPanelInterval() != time.Minute {
		t.Fatal("too many elapsed redraws")
	}
}

func TestWorkPanelMultipleAttentionAndOverflow(t *testing.T) {
	m := conversationShell()
	m.content.Capabilities = append(m.content.Capabilities, protocol.Capability{Name: "progress", Version: "1.0"})
	cards := []state.WorkCard{}
	for i := 0; i < 50; i++ {
		c := state.WorkCard{ID: fmt.Sprint(i), Name: fmt.Sprintf("Work %d", i), Now: fmt.Sprintf("Step %d", i)}
		if i < 3 {
			c.Waiting = 1
		}
		cards = append(cards, c)
	}
	m.content.Blocks = []state.Block{{Topic: "progress", Work: &state.WorkSummary{Cards: cards}}}
	rows := m.workPanelRows(40, 29)
	for i := 0; i < 3; i++ {
		if !strings.Contains(rows[1+2*i], cards[i].Name) {
			t.Fatal("needs-person card hidden")
		}
	}
	rows = m.workPanelRows(40, 4)
	if len(rows) != 4 || !strings.Contains(rows[3], "49") || !strings.Contains(rows[3], fmt.Sprintf(design.Text(m.styles.options.Language, "work.needs"), 2)) {
		t.Fatalf("hidden attention count: %q", rows)
	}
}
