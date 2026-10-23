// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

type panelViewsFixture struct {
	Counts, Widths []int
	Height         int
	Names, States  []string
	Draft          string
}

// E10 drives real keys through the installed TUI, reading Core's replayed ledger
// projection as its oracle. The separate live-worker scenario proves execution.
func TestM3WorkPanelViewsInstalledCore(t *testing.T) {
	var fixture panelViewsFixture
	raw, err := os.ReadFile("../m3/work-panel-views.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, n := range fixture.Counts {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			h, start := installedConversationHarness(t, "http://127.0.0.1:1")
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
			defer cancel()
			h.ctx = ctx
			core := start(true)
			h.stopCore(core)
			seedPanelViews(t, h, n, fixture)
			core = start(true)
			defer h.stopCore(core)
			expected := panelOverview(t, h)
			if len(expected) != n {
				t.Fatalf("Core cards: %d want %d", len(expected), n)
			}
			stateCounts := map[protocol.RunningState]int{}
			for _, row := range expected {
				if row.Line.State == nil || row.Line.Now == nil || row.Line.StartedAt == nil {
					t.Fatalf("missing Core fields: %+v", row)
				}
				stateCounts[*row.Line.State]++
			}
			for _, state := range fixture.States {
				if stateCounts[protocol.RunningState(state)] == 0 {
					t.Fatalf("missing fixture state %s: %v", state, stateCounts)
				}
			}
			for _, language := range []design.Language{design.English, design.Korean} {
				for _, ascii := range []bool{false, true} {
					for _, width := range fixture.Widths {
						env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
						if ascii {
							env = append(env, "LINA_GLYPHS=ascii")
						}
						terminal := h.startTerminalSize(env, false, true, width, fixture.Height)
						waitDraft := func(draft string) {
							t.Helper()
							ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
							defer cancel()
							snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
								return s.AltScreen && !s.CursorHidden && s.CursorY == fixture.Height-2 && strings.Contains(screenText(s), design.Text(language, "live")) && (draft == "" || strings.Contains(screenRows(s)[fixture.Height-2], draft))
							})
							if err != nil {
								t.Fatalf("draft/focus lost: %v\n%s", err, screenText(snap))
							}
						}
						waitDraft("")
						h.keys(terminal, fixture.Draft)
						waitDraft(fixture.Draft)
						x, panelWidth := 80, 40
						if width < 120 {
							x, panelWidth = 0, width
						}
						h.keys(terminal, "\x10")
						filters := []protocol.RunningState{"", protocol.RunningStateNeedsInput, protocol.RunningStateInProgress, protocol.RunningStateWaiting, protocol.RunningStateDone}
						for _, compact := range []bool{true, false} {
							h.keys(terminal, "\x10")
							for _, filter := range filters {
								before := time.Now()
								check := func(s tuiterm.Snapshot) bool {
									rows := panelCellRows(s, x, panelWidth)
									title := design.Text(language, "work.title")
									if filter != "" {
										sep := " · "
										if ascii {
											sep = " - "
										}
										title += sep + design.Text(language, "work.filter."+string(filter))
									}
									if rows[1] != title || !s.CursorHidden {
										return false
									}
									text := strings.Join(rows[1:fixture.Height-4], "\n")
									shown, total, done := 0, 0, 0
									for _, row := range expected {
										line := row.Line
										matches := filter == "" || *line.State == filter
										if matches {
											total++
											if *line.State == protocol.RunningStateDone {
												done++
											}
										}
										found := -1
										for j, r := range rows[2 : fixture.Height-4] {
											if strings.Contains(r, strings.Fields(line.Name)[0]+" ") {
												if found != -1 {
													return false
												}
												found = j + 2
											}
										}
										if found < 0 {
											continue
										}
										if !matches || *line.State == protocol.RunningStateDone {
											return false
										}
										shown++

										started, e := time.Parse(time.RFC3339Nano, *line.StartedAt)
										if e != nil {
											return false
										}
										elapsed := panelElapsed(time.Now().Sub(started))
										if !strings.HasSuffix(rows[found], elapsed) {
											elapsed = panelElapsed(before.Sub(started))
										}
										if !strings.HasSuffix(rows[found], elapsed) {
											return false
										}
										if s.Cells[found][x+panelWidth-1].Content != elapsed[len(elapsed)-1:] {
											return false
										}
										body := strings.TrimSpace(strings.TrimSuffix(rows[found], elapsed))
										body = strings.TrimPrefix(strings.TrimPrefix(body, "› "), "> ")
										if compact {
											valid := false
											for split, r := range body {
												if r == ' ' && panelVisiblePrefix(body[:split], line.Name) && panelVisiblePrefix(strings.TrimSpace(body[split+1:]), *line.Now) {
													valid = true
													break
												}
											}
											if !valid {
												return false
											}
										} else if !panelVisiblePrefix(body, line.Name) || !panelVisiblePrefix(strings.TrimSpace(rows[found+1]), *line.Now) {
											return false
										}
									}
									if total > done && shown == 0 {
										return false
									}
									if done > 0 && !strings.Contains(text, fmt.Sprintf(design.Text(language, "work.done"), done)) {
										return false
									}
									tail := "…"
									if ascii {
										tail = "..."
									}
									hidden := total - done - shown
									more := fmt.Sprintf(design.Text(language, "work.more"), tail, hidden)
									if hidden > 0 && !strings.Contains(text, more) {
										return false
									}
									// Count physical card rows: compact has no second activity row.
									nonempty := 0
									for _, r := range rows[2 : fixture.Height-4] {
										if strings.TrimSpace(r) != "" {
											nonempty++
										}
									}
									want := shown
									if !compact {
										want *= 2
									}
									if done > 0 {
										want++
									}
									if hidden > 0 {
										want++
									}
									return nonempty == want
								}
								ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
								snap, err := terminal.screen.Wait(ctx, check)
								cancel()
								if err != nil {
									t.Fatalf("E10 %s/ascii=%t/%d compact=%t filter=%s: %v\n%s", language, ascii, width, compact, filter, err, screenText(snap))
								}
								t.Logf("E10 N=%d %s ascii=%t %dx%d compact=%t filter=%s\n%s", n, language, ascii, width, fixture.Height, compact, filter, screenText(snap))
								h.keys(terminal, "\x1b[C")
							}
						}
						h.keys(terminal, "\x1b")
						waitDraft(fixture.Draft)
						h.keys(terminal, "\x04")
						terminal.process.wait(t, 0)
						h.expectTerminalRestored(terminal)
					}
				}
			}
		})
	}
}

func seedPanelViews(t *testing.T, h *harness, n int, fixture panelViewsFixture) {
	t.Helper()
	db, closeStore := installedAcceptanceStore(t, h)
	defer closeStore()
	var person, agent string
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(h.root, "sample.txt")
	content := []byte("verified fixture result\n")
	if err := os.WriteFile(artifact, content, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(content))
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("view-%02d", i)
			state := fixture.States[i%len(fixture.States)]
			// The last admitted card always needs the person, as in the panel corpus.
			if i == n-1 {
				state = "needs_input"
			}
			task := work.Task{ID: id, PersonID: person, AgentID: agent, ConversationID: "main:" + agent, Title: fmt.Sprintf("W%02d %s", i, fixture.Names[i%len(fixture.Names)]), Done: "Sample verified", ArtifactRoots: []string{h.root}}
			run := work.Run{ID: id + ":run", TaskID: id, Generation: 1, Worker: "fixture", ThreadID: id, TurnID: id, NodeID: "fixture"}
			at := time.Now().UTC().Add(-42 * time.Minute)
			if state == "waiting" {
				task.Schedule = &work.Schedule{Group: "views", Repository: "fixture", Worker: run.Worker, Host: run.NodeID}
			}
			events := []work.Event{{Key: id + ":create", TaskID: id, Kind: "created", Task: &task, RecordedAt: at.Format(time.RFC3339Nano)}}
			add := func(e work.Event) {
				e.Key = fmt.Sprintf("%s:%d", id, len(events))
				e.TaskID = id
				e.RecordedAt = at.Add(time.Minute).Format(time.RFC3339Nano)
				events = append(events, e)
			}
			if state == "needs_input" {
				add(work.Event{Kind: "waiting_opened", Wait: &work.Wait{Round: "sample", Question: "Choose the label"}})
			} else {
				if task.Schedule != nil {
					add(work.Event{Kind: "schedule_reserved", Scheduling: &work.Scheduling{Reserved: true, CapacityHeld: true}})
				}
				add(work.Event{Kind: "started", Run: &run})
				receipt := work.Receipt{SchemaVersion: 1, RelationshipID: id, ExecutionGeneration: 1, Attempt: 1, Outcome: "ready_for_review", Producer: "child", TurnRef: work.TurnRef{ThreadID: id, TurnID: id, TurnStatus: "completed"}, EmittedAt: at, Manifest: []work.Artifact{{Path: artifact, SHA256: hash}}, Provenance: []work.Provenance{{ArtifactID: id, TaskID: id, NodeID: run.NodeID, Path: artifact, SHA256: hash, ObservedAt: at}}}
				receipt.RevisionHash = work.ManifestHash(receipt.Manifest)
				receipt.EventID = work.ReceiptID(receipt)
				add(work.Event{Kind: "receipt", RunID: run.ID, Receipt: &receipt})
				add(work.Event{Kind: "finished", RunID: run.ID, Value: "completed"})
				if state == "done" {
					add(work.Event{Kind: "judged", ReceiptID: receipt.EventID, Value: "verified", Evidence: "fixture content checked"})
				}
				if state == "waiting" {
					add(work.Event{Kind: "schedule_wait", Scheduling: &work.Scheduling{}, WaitReason: &work.ScheduleWait{Kind: "host_capacity", Target: "fixture"}})
				}
			}
			card, err := work.Project(events)
			if err != nil {
				return err
			}
			if card.Active {
				return fmt.Errorf("fixture cannot leave an unobserved live execution")
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

// Check content independently of the renderer's clipping budgets.
func panelVisiblePrefix(shown, full string) bool {
	for _, tail := range []string{"…", "..."} {
		if strings.HasSuffix(shown, tail) {
			prefix := strings.TrimSuffix(shown, tail)
			return prefix != "" && strings.HasPrefix(full, prefix)
		}
	}
	return shown == full
}
