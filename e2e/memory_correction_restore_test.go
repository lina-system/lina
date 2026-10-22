// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
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
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
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
	raw, err := os.ReadFile("m1/memory-correction-restore.json")
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
			h, start := installConversationBundle(t, newHarness(t, binary), bundle, provider.URL)
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

// Drive the actual TUI and wait for each management result to render before
// closing its view. The tap observes the same committed result the TUI receives.
func correctionMemoryTUI(t *testing.T, h *harness) (func(string) protocol.MemoryResult, func()) {
	t.Helper()
	socket := h.socket
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
				// The in-flight help already contains the command examples.
				// Wait for the response-only heading before closing its pager.
				if result.Operation == nil {
					t.Fatal("memory result has no operation")
				}
				heading := design.Text(design.English, *result.Operation)
				if _, err := terminal.screen.Wait(h.ctx, func(s tuiterm.Snapshot) bool {
					return strings.TrimSpace(screenRows(s)[0]) == heading
				}); err != nil {
					t.Fatalf("memory result was not rendered: %v", err)
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
