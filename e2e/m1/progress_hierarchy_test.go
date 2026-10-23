// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Installed Core rebuilds synthetic durable work, then serves both the direct
// overview and its subscribed snapshot. No provider or worker credentials are used.
func TestM3ProgressHierarchyInstalledCore(t *testing.T) {
	var fixture struct {
		Tasks []struct {
			ID, Parent, Title string
			NeedsInput        bool `json:"needs_input"`
		}
	}
	raw, err := os.ReadFile("../m3/progress-hierarchy.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h.ctx = ctx
	core := start(true)
	h.stopCore(core)
	db, closeStore := installedAcceptanceStore(t, h)
	var person, agent string
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT r.person_id,f.agent_id FROM agent_first_run f JOIN agent_revisions r ON r.agent_id=f.agent_id AND r.revision=1").Scan(&person, &agent)
	}); err != nil {
		t.Fatal(err)
	}
	parents := map[string]string{}
	if err := db.Write(ctx, func(tx *canon.Tx) error {
		for _, item := range fixture.Tasks {
			parents[item.ID] = item.Parent
			task := work.Task{ID: item.ID, GoalID: item.Parent, PersonID: person, AgentID: agent, ConversationID: "main:" + agent, Title: item.Title, Done: "Sample verified"}
			run := work.Run{ID: item.ID + ":run", TaskID: item.ID, Generation: 1, Worker: "fixture", ThreadID: item.ID, TurnID: item.ID, NodeID: "local"}
			events := []work.Event{
				{Key: item.ID + ":create", TaskID: item.ID, Kind: "created", Task: &task},
			}
			if item.NeedsInput {
				events = append(events, work.Event{Key: item.ID + ":wait", TaskID: item.ID, Kind: "waiting_opened", Wait: &work.Wait{Round: "sample", Question: item.Title}})
			} else {
				events = append(events, work.Event{Key: item.ID + ":start", TaskID: item.ID, Kind: "started", Run: &run})
			}
			if _, err := work.Project(events); err != nil {
				return err
			}
			for _, event := range events {
				raw, err := json.Marshal(event)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, "INSERT INTO work_ledger(task_id,event_key,record) VALUES(?,?,?)", item.ID, event.Key, raw); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	closeStore()
	assertHierarchy := func(rows *[]protocol.ProgressRow) {
		t.Helper()
		if rows == nil || len(*rows) != len(parents) {
			t.Fatalf("missing work rows: %+v", rows)
		}
		seen := map[string]bool{}
		group := ""
		for _, row := range *rows {
			parent, ok := parents[row.Ref]
			if !ok || seen[row.Ref] {
				t.Fatalf("unexpected or repeated row %s", row.Ref)
			}
			seen[row.Ref] = true
			if parent == "" {
				group = row.Ref
				if row.Line.Depth != "0" {
					t.Fatalf("root %s depth %s", row.Ref, row.Line.Depth)
				}
			} else if group != parent || row.Line.Depth != "1" {
				t.Errorf("child %s beneath %s at depth %s, want %s at depth 1", row.Ref, group, row.Line.Depth, parent)
			}
		}
	}
	assertCards := func(order *[]string) {
		t.Helper()
		want := []string{"b-needs", "a-needs", "b-running", "a-running", "c-standalone"}
		if order == nil || !reflect.DeepEqual(*order, want) {
			t.Fatalf("flat card order = %v, want %v", order, want)
		}
	}
	for _, phase := range []string{"initial", "restart"} {
		core = start(true)
		func() {
			defer h.stopCore(core)
			c, err := clientapi.DialWithCapabilities(ctx, h.socket, "progress-hierarchy", []protocol.Capability{{Name: "progress", Version: "1.0"}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			view := "overview"
			if _, err := c.Progress(ctx, protocol.ProgressGet{View: &view}); err != nil {
				t.Fatal(err)
			}
			e, err := c.Receive(ctx)
			p, ok := e.Payload.(protocol.Progress)
			if err != nil || e.Error != nil || !ok {
				t.Fatalf("overview: %+v %v", e, err)
			}
			assertHierarchy(p.Rows)
			assertCards(p.CardOrder)
			topic := "progress"
			if _, err := c.Subscribe(ctx, protocol.Subscribe{Topic: &topic}); err != nil {
				t.Fatal(err)
			}
			e, err = c.Receive(ctx)
			snapshot, ok := e.Payload.(protocol.Snapshot)
			if err != nil || e.Error != nil || !ok || len(snapshot.Items) != 1 {
				t.Fatalf("progress snapshot: %+v %v", e, err)
			}
			var update protocol.ProgressUpdate
			if err := json.Unmarshal(snapshot.Items[0].Payload, &update); err != nil {
				t.Fatal(err)
			}
			assertHierarchy(update.Rows)
			assertCards(update.CardOrder)
			if phase == "initial" {
				for _, language := range []design.Language{design.English, design.Korean} {
					for _, ascii := range []bool{false, true} {
						for _, width := range []int{120, 100} {
							env := []string{"TERM=xterm-256color", "LANG=" + string(language) + "_US.UTF-8", "LC_ALL=" + string(language) + "_US.UTF-8"}
							if ascii {
								env = append(env, "LINA_GLYPHS=ascii")
							}
							terminal := h.startTerminalSize(env, false, true, width, 34)
							if _, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
								return s.AltScreen && !s.CursorHidden && strings.Contains(screenText(s), design.Text(language, "live"))
							}); err != nil {
								t.Fatal(err)
							}
							x, panelWidth := 80, 40
							if width < 120 {
								h.keys(terminal, "\x10")
								x, panelWidth = 0, width
							}
							snap, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
								rows := panelCellRows(s, x, panelWidth)
								for i, title := range []string{"Choose release date", "Choose venue", "Build release", "Draft workshop", "Read guide"} {
									if !strings.Contains(rows[2+2*i], title) {
										return false
									}
								}
								return true
							})
							if err != nil {
								t.Fatalf("card order %s/ascii=%t/width=%d: %v\n%s", language, ascii, width, err, screenText(snap))
							}
							h.keys(terminal, "\x04")
							terminal.process.wait(t, 0)
							h.expectTerminalRestored(terminal)
						}
					}
				}
			}
			t.Logf("%s: overview and subscribed snapshot contain %d work rows", phase, len(*p.Rows))
		}()
	}
}
