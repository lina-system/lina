// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/compaction"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1MemoryCorrectionForgetRestore(t *testing.T) {
	var scenario struct {
		Facts            []string
		Direct, Retained string
		Depths           []int
	}
	raw, err := os.ReadFile("memory-correction-restore.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	bundle := makeInstallBundle(t, t.TempDir(), binary, buildCommand(t, "lina-node"))
	for _, depth := range scenario.Depths {
		t.Run(fmt.Sprintf("corrections-%d", depth), func(t *testing.T) {
			requests := make(chan []byte, 8)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				requests <- body
				emitResponse(w, "Synthetic fixture reply", true)
			}))
			defer provider.Close()
			h := newHarness(t, binary)
			// Multiple Core restarts, backup/restore and terminal sessions share
			// this scenario; the default three-operation harness budget is too short.
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			t.Cleanup(cancel)
			h.ctx = ctx
			h, start := installConversationBundle(t, h, bundle, provider.URL)
			core := start(true)
			command, closeTUI := correctionMemoryTUI(t, h)
			first := command("/memory remember --holder=all --scope=local " + scenario.Facts[0])
			direct := command("/memory remember --holder=agent --scope=private " + scenario.Direct)
			retained := command("/memory remember " + scenario.Retained)
			if first.ItemID == nil || direct.ItemID == nil || retained.ItemID == nil {
				t.Fatal("missing memory identity")
			}
			closeTUI()
			h.stopCore(core)
			summaryID := seedForgottenSummary(t, h, *first.ItemID, scenario.Facts[0])
			backup := h.command("backup", "--state-root", h.root)
			backup.start(t)
			backup.wait(t, 0)
			generations, err := filepath.Glob(filepath.Join(h.root, "backups/generation-*"))
			if err != nil || len(generations) != 1 {
				t.Fatalf("generation: %v %v", generations, err)
			}
			core = start(true)
			command, closeTUI = correctionMemoryTUI(t, h)
			latest := first.ItemID
			for _, fact := range scenario.Facts[1 : depth+1] {
				result := command("/memory correct " + *latest + " " + fact)
				if result.ItemID == nil || *result.ItemID == *latest {
					t.Fatal("correction did not create a successor")
				}
				latest = result.ItemID
			}
			command("/memory forget " + *latest)
			command("/memory forget " + *direct.ItemID)
			assertList := func() {
				t.Helper()
				result := command("/memory list")
				if result.Items == nil || len(*result.Items) != 1 || (*result.Items)[0].ItemID != *retained.ItemID {
					t.Errorf("forgotten correction chain reappeared in /memory list: %+v", result.Items)
				}
			}
			assertRequest := func(stage string) {
				t.Helper()
				reader, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				assertForgottenSummary(t, reader, summaryID)
				if err := reader.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				client, conversation := conversationClient(t, h)
				admittedTurn(t, h, client, conversation, stage, "Synthetic context probe")
				client.Close()
				select {
				case body := <-requests:
					for _, fact := range append(append([]string{}, scenario.Facts...), scenario.Direct) {
						if strings.Contains(string(body), fact) {
							t.Errorf("%s provider request exposed forgotten fact %q", stage, fact)
						}
					}
					if !strings.Contains(string(body), scenario.Retained) {
						t.Error("provider request omitted retained memory")
					}
				case <-h.ctx.Done():
					t.Fatal("missing provider request")
				}
			}
			assertList()
			closeTUI()
			assertRequest("before-restore")
			h.stopCore(core)

			// Travel only with the generation and its sibling removals. The target has
			// the same installed identity but none of the newer source canon.
			source := h.root
			other := newHarness(t, h.binary)
			for _, name := range []string{"system/core/main.json", "system/core/epoch", "system/bin/manifest.json"} {
				data, err := os.ReadFile(filepath.Join(source, name))
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(other.root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			h = other
			restore := h.command("restore", "--state-root", h.root, "--generation", generations[0])
			restore.start(t)
			restore.wait(t, 0)
			// Recall the published restored canon before Core can run any maintenance.
			db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			var person, agent string
			err = db.Read(h.ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(h.ctx, "SELECT person,json_extract(record,'$.source_agent') FROM memory_item WHERE id=?", *first.ItemID).Scan(&person, &agent)
			})
			if err != nil {
				t.Fatal(err)
			}
			scope := memory.RecallScope{Person: person, Agent: agent}
			assertForgottenSummary(t, db, summaryID)
			hits, err := memory.New(db).Recall(h.ctx, scope, memory.Query{At: time.Now().Unix(), History: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 || hits[0].ID != *retained.ItemID {
				t.Errorf("restored recall exposed correction history: %+v", hits)
			}
			for _, fact := range append(append([]string{}, scenario.Facts...), scenario.Direct) {
				answer, err := memoryexec.NewMemory(db, nil, nil).RecallPerson(h.ctx, scope, memoryexec.PersonRecall{Question: "Synthetic recall probe", Query: fact, At: time.Now().Unix(), History: true})
				if err != nil {
					t.Fatal(err)
				}
				if len(answer.Sources) != 0 {
					t.Errorf("restored recall or ledger fallback exposed %q: %+v", fact, answer.Sources)
				}
			}
			if err := db.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			core = h.command("core", "--state-root", h.root, "--socket", h.socket)
			core.command.Env = append(core.command.Env, "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
			core.start(t)
			h.connect(core).Close()
			waitCoreReady(t, h)
			command, closeTUI = correctionMemoryTUI(t, h)
			assertList()
			closeTUI()
			assertRequest("after-restore")
			h.stopCore(core)
		})
	}
}

func seedForgottenSummary(t *testing.T, h *harness, itemID, fact string) string {
	t.Helper()
	db, close := installedAcceptanceStore(t, h)
	defer close()
	var record []byte
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT record FROM memory_item WHERE id=?", itemID).Scan(&record)
	}); err != nil {
		t.Fatal(err)
	}
	var item memory.Item
	if err := json.Unmarshal(record, &item); err != nil || len(item.Evidence) == 0 {
		t.Fatalf("memory evidence: %v", err)
	}
	event, err := ledger.New(db, nil).Get(h.ctx, item.Evidence[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	s := compaction.New(db)
	frontier, err := s.Frontier(h.ctx, event.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	var cover []compaction.Coverage
	if err := db.Read(h.ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(h.ctx, "SELECT id FROM conv_event WHERE conversation_id=? ORDER BY rowid", event.ConversationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			cover = append(cover, compaction.Coverage{EventID: id})
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	id := "acceptance-forgotten-summary"
	_, err = s.Publish(h.ctx, compaction.Proposal{Expected: frontier, ID: id, Cover: cover, Output: compaction.Output{Model: "synthetic", PromptVersion: "1", BuiltAt: time.Now(), Text: fact, Dropped: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if node, err := s.Node(h.ctx, id); err != nil || node.Output.Text != fact {
		t.Fatalf("summary positive control: %+v %v", node, err)
	}
	return id
}

func assertForgottenSummary(t *testing.T, db *canon.Store, id string) {
	t.Helper()
	if _, err := compaction.New(db).Node(t.Context(), id); !errors.Is(err, compaction.ErrUnavailable) {
		t.Fatalf("forgotten summary is readable: %v", err)
	}
}

// Drive the actual TUI and wait for each management result to render before
// closing its view. The tap observes the same committed result the TUI receives.
func correctionMemoryTUI(t *testing.T, h *harness) (func(string) protocol.MemoryResult, func()) {
	t.Helper()
	socket := h.socket
	waitCoreReady(t, h)
	tap := newAccountTap(t, h, socket, true)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color")
	waitComposerReady(t, h, terminal)
	opened := false
	command := func(text string) protocol.MemoryResult {
		t.Helper()
		if opened {
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
		}
		h.keys(terminal, text+"\r")
		for {
			e := tap.next(t, h)
			if e.Error != nil {
				t.Fatalf("%s refused: %s", text, e.Error.Code)
			}
			if result, ok := e.Payload.(protocol.MemoryResult); ok {
				if result.Operation == nil {
					t.Fatal("memory reply has no operation")
				}
				// The shared help text can still belong to the previous reply.
				// Observe this result's heading before closing its view, otherwise
				// its delayed render can reopen over the next command's composer.
				marker := design.Text(design.English, *result.Operation)
				if result.ItemID != nil {
					// Correct and forget share the same id. Wait for this operation's
					// rendered row, not the previous result still on screen.
					marker = design.Text(design.English, *result.Operation) + " " + *result.ItemID
				} else if result.Items != nil && len(*result.Items) > 0 {
					item := (*result.Items)[0]
					marker = item.ItemID + "  " + item.Holder
				}
				// A returned item id can already appear in the command draft.
				// Wait for the result pager as well as its text before sending Esc.
				view, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
					rows := screenRows(s)
					return s.CursorHidden && len(rows) > 0 && strings.Contains(rows[len(rows)-1], "Esc") && strings.Contains(screenText(s), marker)
				})
				if err != nil {
					t.Fatalf("memory result pager missing: %v\n%s", err, screenText(view))
				}
				opened = true
				return result
			}
		}
	}
	return command, func() {
		h.keys(terminal, "\x04")
		terminal.process.wait(t, 0)
		h.expectTerminalRestored(terminal)
		tap.close()
		h.socket = socket
	}
}
