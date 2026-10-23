// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/tui/render"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func panelOverview(t *testing.T, h *harness) []protocol.ProgressRow {
	t.Helper()
	c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "work-panel", []protocol.Capability{{Name: "progress", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	view := "overview"
	if _, err = c.Progress(h.ctx, protocol.ProgressGet{View: &view}); err != nil {
		t.Fatal(err)
	}
	e, err := c.Receive(h.ctx)
	if err != nil || e.Error != nil {
		t.Fatalf("progress: %+v %v", e, err)
	}
	p, ok := e.Payload.(protocol.Progress)
	if !ok || p.Rows == nil {
		t.Fatalf("missing overview: %+v", e)
	}
	return *p.Rows
}

func panelCellRows(s tuiterm.Snapshot, x, width int) []string {
	rows := []string{}
	for _, row := range s.Cells {
		var b strings.Builder
		for _, cell := range row[x:min(len(row), x+width)] {
			b.WriteString(cell.Content)
		}
		rows = append(rows, strings.TrimRight(b.String(), " "))
	}
	return rows
}

// Installed Core replays dated fixture work, authorizes the progress topic and
// delivers it to the real PTY. Recovered executions correctly remain unknown.
// The live-worker scenario below covers transitions during actual execution.
func TestM3WorkPanelInstalledCoreMatrix(t *testing.T) {
	var fixture struct {
		Counts, Widths []int
		Height         int
		Names          []string
	}
	raw, err := os.ReadFile("../m3/work-panel.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil {
		t.Fatal("fixture", err)
	}
	for _, n := range fixture.Counts {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			h, start := installedConversationHarness(t, "http://127.0.0.1:1")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			h.ctx = ctx
			core := start(true)
			h.stopCore(core)
			seedPanelWork(t, h, n, fixture.Names)
			core = start(true)
			defer h.stopCore(core)
			expected := panelOverview(t, h)
			if len(expected) != n || expected[0].Line.Waiting != "1" {
				t.Fatalf("Core order/count: %+v", expected)
			}
			for _, language := range []design.Language{design.English, design.Korean} {
				for _, ascii := range []bool{false, true} {
					for _, width := range fixture.Widths {
						env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
						if ascii {
							env = append(env, "LINA_GLYPHS=ascii")
						}
						beforeFrame := time.Now()
						terminal := h.startTerminalSize(env, false, true, width, fixture.Height)
						// A visible composer proves that progress did not take focus on connection.
						_, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
							return s.AltScreen && !s.CursorHidden && strings.Contains(screenText(s), design.Text(language, "live"))
						})
						if err != nil {
							t.Fatal(err)
						}
						if width < 120 {
							h.keys(terminal, "\x10")
						}
						title := expected[0].Line.Name
						x, panelWidth := 80, 40
						if width < 120 {
							x = 0
							panelWidth = width
						}
						snap, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
							rows := panelCellRows(s, x, panelWidth)
							return rows[1] == design.Text(language, "work.title") && strings.Contains(rows[2], title)
						})
						if err != nil {
							t.Fatalf("panel frame: %v\n%s", err, screenText(snap))
						}
						rows := panelCellRows(snap, x, panelWidth)
						if rows[1] != design.Text(language, "work.title") || !strings.Contains(rows[2], title) {
							t.Fatalf("E1 panel absent %s/ascii=%t/%d:\n%s", language, ascii, width, screenText(snap))
						}
						shown := 0
						for i := 2; i+1 < len(rows) && strings.HasPrefix(rows[i+1], "  "); i += 2 {
							line := expected[shown].Line
							if line.StartedAt == nil || line.Now == nil {
								t.Fatal("Core omitted clock/activity")
							}
							startTime, err := time.Parse(time.RFC3339Nano, *line.StartedAt)
							if err != nil {
								t.Fatal(err)
							}
							elapsed := panelElapsed(time.Now().Sub(startTime))
							if !strings.HasSuffix(rows[i], elapsed) && !strings.HasSuffix(rows[i], panelElapsed(beforeFrame.Sub(startTime))) {
								t.Fatalf("E3 elapsed %q want %s", rows[i], elapsed)
							}
							if !strings.HasPrefix(*line.Now, strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(rows[i+1], "..."), "…"))) {
								t.Fatalf("E4 Now differs: %q vs %q", rows[i+1], *line.Now)
							}
							shown++
						}
						if shown == 0 || shown > n {
							t.Fatalf("E2 shown %d / %d", shown, n)
						}
						if n > shown {
							tail := "…"
							if ascii {
								tail = "..."
							}
							want := fmt.Sprintf(design.Text(language, "work.more"), tail, n-shown)
							if !strings.Contains(strings.Join(rows, "\n"), want) {
								t.Fatalf("E5 overflow missing %s:\n%s", want, screenText(snap))
							}
						}
						if !strings.Contains(screenText(snap), fmt.Sprintf(design.Text(language, "work.count"), 0, 1)) {
							t.Fatalf("E5 status mismatch:\n%s", screenText(snap))
						}
						t.Logf("E1-E5 %s ascii=%t %dx%d N=%d shown=%d\n%s", language, ascii, width, fixture.Height, n, shown, screenText(snap))
						h.keys(terminal, "\x04")
						terminal.process.wait(t, 0)
						h.expectTerminalRestored(terminal)
					}
				}
			}
		})
	}
}

func seedPanelWork(t *testing.T, h *harness, n int, names []string) {
	t.Helper()
	db, close := installedAcceptanceStore(t, h)
	defer close()
	var person, agent string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("panel-%03d", i)
			title := names[i%len(names)]
			if i == n-1 {
				title = "Needs reply"
			}
			task := work.Task{ID: id, PersonID: person, AgentID: agent, ConversationID: "main:" + agent, Title: title, Done: "Sample verified"}
			at := time.Now().UTC().Add(-time.Duration(42+i*65) * time.Minute)
			events := []work.Event{{Key: id + ":create", TaskID: id, Kind: "created", Task: &task, RecordedAt: at.Format(time.RFC3339Nano)}}
			if i == n-1 {
				events = append(events, work.Event{Key: id + ":wait", TaskID: id, Kind: "waiting_opened", Wait: &work.Wait{Round: "sample", Question: "Choose the label"}})
			} else {
				run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "local"}
				events = append(events, work.Event{Key: id + ":start", TaskID: id, Kind: "started", Run: &run, RecordedAt: at.Format(time.RFC3339Nano)})
			}
			if _, err := work.Project(events); err != nil {
				return err
			}
			for _, event := range events {
				raw, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if _, err = tx.ExecContext(h.ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", id, event.Key, raw); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func panelElapsed(d time.Duration) string {
	m := int64(max(0, d) / time.Minute)
	if m >= 1440 {
		return fmt.Sprintf("%dd %02dh", m/1440, m/60%24)
	}
	if m >= 60 {
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
	return fmt.Sprintf("%dm", m)
}

func TestM3WorkPanelNavigationInstalledCore(t *testing.T) {
	for _, width := range []int{120, 100} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			h, start := installedConversationHarness(t, "http://127.0.0.1:1")
			core := start(true)
			h.stopCore(core)
			names := make([]string, 50)
			for i := range names {
				names[i] = fmt.Sprintf("Navigation-%02d", i)
			}
			seedPanelWork(t, h, len(names), names)
			core = start(true)
			defer h.stopCore(core)
			expected := panelOverview(t, h)
			if len(expected) != len(names) {
				t.Fatalf("Core returned %d cards, want %d", len(expected), len(names))
			}
			terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8"}, false, true, width, 34)
			waitResizeChrome(t, h, terminal, 34, "")
			h.keys(terminal, "keep draft")
			waitResizeChrome(t, h, terminal, 34, "keep draft")
			h.keys(terminal, "\x10")
			x, panelWidth := 0, width
			if width == 120 {
				x, panelWidth = 80, 40
			}
			waitCard := func(index int, absent string) {
				t.Helper()
				line := expected[index].Line
				if line.Now == nil {
					t.Fatal("Core omitted activity")
				}
				ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
				defer cancel()
				snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
					rows := strings.Join(panelCellRows(s, x, panelWidth), "\n")
					return s.CursorHidden && strings.Contains(rows, line.Name) && strings.Contains(rows, *line.Now) && (absent == "" || !strings.Contains(rows, absent))
				})
				if err != nil {
					t.Fatalf("card %d missing: %v\n%s", index, err, screenText(snap))
				}
			}
			waitCard(0, expected[len(expected)-1].Line.Name)
			h.keys(terminal, strings.Repeat("\x1b[B", len(expected)-1))
			waitCard(len(expected)-1, expected[0].Line.Name)
			h.keys(terminal, strings.Repeat("\x1b[A", len(expected)-1))
			waitCard(0, expected[len(expected)-1].Line.Name)
			h.keys(terminal, "\x1b")
			waitResizeChrome(t, h, terminal, 34, "keep draft")
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
		})
	}
}

func TestM3WorkPanelLiveCodexWorker(t *testing.T) {
	var terminal *terminal
	var initial, activity string
	runCodexWorkTools(t, "../m3/work-panel.json", "", func(h *harness, phase string, _ []protocol.Question) {
		if phase == "approval" || phase == "question" {
			rows := panelOverview(t, h)
			if len(rows) != 1 || rows[0].Line.StartedAt == nil || rows[0].Line.Now == nil {
				t.Fatalf("live Core fields missing: %+v", rows)
			}
			line := rows[0].Line
			if phase == "approval" {
				initial = *line.StartedAt
				terminal = h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 120, 34)
			} else if initial != *line.StartedAt || activity == *line.Now {
				t.Fatalf("start changed or activity stuck: %+v", line)
			}
			activity = *line.Now
			expected := activity
			u := render.Unicode{}
			if u.Width(expected) > 38 {
				expected = render.Truncate(expected, 37, u) + "…"
			}
			snap, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
				rows := panelCellRows(s, 80, 40)
				return strings.Contains(rows[2], line.Name) && strings.TrimSpace(rows[3]) == expected
			})
			if err != nil {
				t.Fatalf("live card %s: %v\n%s", phase, err, screenText(snap))
			}
			started, _ := time.Parse(time.RFC3339Nano, initial)
			if !strings.HasSuffix(panelCellRows(snap, 80, 40)[2], panelElapsed(time.Now().Sub(started))) {
				t.Fatalf("live elapsed fabricated: %s", screenText(snap))
			}
			t.Logf("live Core-launched Codex %s\n%s", phase, screenText(snap))
		}
		if phase == "completed" {
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
		}
	})
}
