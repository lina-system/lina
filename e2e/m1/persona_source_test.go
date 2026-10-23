// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/persona"
)

func setPersonaSource(t *testing.T, root, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "system/core/config.json"), mustJSON(t, map[string]string{"persona_source": source}), 0600); err != nil {
		t.Fatal(err)
	}
}

// Build a real Core with synthetic embedded assets. No runtime test override or
// private-source hook is added to the product; Go's build overlay changes only
// these disposable build inputs.
func buildPersonaFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	replace := map[string]string{}
	for _, s := range persona.Starters() {
		for _, layer := range []string{"core-v1.md", "default-voice-v1.md"} {
			path := filepath.Join(directory, s.Key+"-"+layer)
			text := s.Key + " fixture " + layer
			if s.Key == "legal" && layer == "default-voice-v1.md" {
				text = " "
			}
			if err := os.WriteFile(path, []byte(text), 0600); err != nil {
				t.Fatal(err)
			}
			replace[filepath.Join(source, "persona", s.Key, layer)] = path
		}
	}
	overlay := filepath.Join(directory, "overlay.json")
	if err := os.WriteFile(overlay, mustJSON(t, map[string]any{"Replace": replace}), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "lina")
	command := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-overlay", overlay, "-o", binary, "./cmd/lina")
	command.Dir = source
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic Core: %v\n%s", err, out)
	}
	return binary
}

// Seed existing agents while Core is stopped. Fresh starter creation belongs to
// its own acceptance scenario; here every preset must migrate and prepare.
func seedShippedPersonaAgents(t *testing.T, h *harness, person string) map[string]string {
	t.Helper()
	ctl, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	epoch, err := ctl.Epoch()
	if err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: epoch, Controls: ctl, Migrations: boot.ApplicationMigrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ids := map[string]string{}
	for _, s := range persona.Starters() {
		if err := store.UpdatePreset(h.ctx, agents.Preset{ID: s.Key, Core: "stale private fixture", DefaultVoice: "stale voice fixture"}, person); err != nil {
			t.Fatal(err)
		}
		a, err := store.Create(h.ctx, agents.Create{Name: s.Name, PersonID: person, Preset: s.Key})
		if err != nil {
			t.Fatal(err)
		}
		ids[s.Key] = a.ID
	}
	return ids
}

func TestM1ShippedPersonaInstallAndTools(t *testing.T) {
	t.Parallel()
	for _, synthetic := range []bool{false, true} {
		t.Run(fmt.Sprintf("synthetic=%t", synthetic), func(t *testing.T) {
			calls := map[string]int{}
			requests := make(chan string, 32)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string
					Input        []json.RawMessage
					Tools        []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				var identity struct {
					AgentID string        `json:"agent_id"`
					Name    string        `json:"name"`
					Owner   string        `json:"owner_person_id"`
					Layers  agents.Layers `json:"layers"`
				}
				if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &identity); err != nil {
					t.Error(err)
				}
				if identity.AgentID == "" || identity.Name == "" || identity.Owner == "" {
					t.Error("identity lost")
				}
				assertEnabledToolNames(t, body.Instructions, body.Tools)
				calls[identity.AgentID]++
				if calls[identity.AgentID]%2 == 1 {
					requests <- body.Instructions
					emitPersonaCall(w, "index-"+identity.AgentID, "skill_index", map[string]any{})
				} else {
					var result struct{ Type, Output string }
					if len(body.Input) == 0 {
						t.Error("missing tool result")
					} else {
						json.Unmarshal(body.Input[len(body.Input)-1], &result)
						if result.Type != "function_call_output" || !strings.Contains(result.Output, "persona-change") {
							t.Errorf("skill tool did not return catalog: %s", result.Output)
						}
					}
					emitResponse(w, "Fixture inventory checked.", true)
				}
			}))
			defer server.Close()
			binary := buildLina(t)
			if synthetic {
				binary = buildPersonaFixture(t)
			}
			h := newHarness(t, binary)
			bundle := makeInstallBundle(t, h.root, h.binary, buildCommand(t, "lina-node"))
			// The legacy checkout contains valid-looking private text. It must never
			// replace shipped text, including when one shipped pair is invalid.
			legacy := t.TempDir()
			dir := filepath.Join(legacy, "characters/lina/persona")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"core-v1.md", "default-voice-v1.md"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("legacy private fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			_, start := installConversationBundle(t, h, bundle, server.URL, "--persona-source", legacy)
			core := start(true)
			client, _ := conversationClient(t, h)
			person := client.Welcome.PersonID
			client.Close()
			h.stopCore(core)
			ids := seedShippedPersonaAgents(t, h, person)
			// An unavailable private checkout also cannot clear working layers.
			if err := os.RemoveAll(legacy); err != nil {
				t.Fatal(err)
			}
			core = start(true)
			client, _ = conversationClient(t, h)
			for _, s := range persona.Starters() {
				conversation := "main:" + ids[s.Key]
				topic := "conversation:" + conversation
				if _, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
					t.Fatal(err)
				}
				if e, err := client.Receive(h.ctx); err != nil || e.Kind != protocol.KindSnapshot {
					t.Fatal("no snapshot", err)
				}
				for turn := range 2 {
					frames := admittedTurn(t, h, client, conversation, fmt.Sprintf("%s-%d", s.Key, turn), "List the available skills.")
					got := false
					for _, f := range frames {
						if item, ok := f.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == "Fixture inventory checked." {
							got = true
						}
					}
					if !got {
						t.Fatal("tool-backed answer did not return to conversation")
					}
					var instructions string
					select {
					case instructions = <-requests:
					case <-h.ctx.Done():
						t.Fatal("provider not called")
					}
					var p struct {
						Name   string        `json:"name"`
						Layers agents.Layers `json:"layers"`
					}
					if err := json.Unmarshal(instructionBlock(instructions, "persona"), &p); err != nil {
						t.Fatal(err)
					}
					wantCore, wantVoice, err := persona.Load(persona.Shipped(), s.Key)
					if err != nil {
						t.Fatal(err)
					}
					if synthetic {
						wantCore = s.Key + " fixture core-v1.md"
						wantVoice = s.Key + " fixture default-voice-v1.md"
						if s.Key == "legal" {
							wantCore, wantVoice = "", ""
						}
					}
					if p.Name != s.Name || p.Layers != (agents.Layers{Core: wantCore, DefaultVoice: wantVoice}) {
						t.Fatalf("wrong prepared pair for %s", s.Key)
					}
					if strings.Contains(instructions, "private fixture") || strings.Contains(instructions, "stale voice fixture") {
						t.Fatal("legacy or stale material injected")
					}
					setPersonaSource(t, h.root, "")
				}
			}
			client.Close()
			h.stopCore(core)
			warnings := strings.Count(core.stderr.String(), "Shipped persona ")
			wantWarnings := 0
			if synthetic {
				wantWarnings = 1
			}
			if warnings != wantWarnings || strings.Contains(core.stderr.String(), legacy) {
				t.Fatalf("warnings=%d want=%d", warnings, wantWarnings)
			}
		})
	}
}

// Names identify the exact native declarations without duplicating schemas.
func assertEnabledToolNames(t *testing.T, instructions string, tools []json.RawMessage) {
	t.Helper()
	var enabled []string
	if err := json.Unmarshal(instructionBlock(instructions, "enabled_tools"), &enabled); err != nil || enabled == nil || len(enabled) != len(tools) {
		t.Errorf("enabled tool names differ from native declarations: %v", err)
		return
	}
	for i, raw := range tools {
		var tool struct{ Name string }
		if err := json.Unmarshal(raw, &tool); err != nil || tool.Name == "" || enabled[i] != tool.Name {
			t.Errorf("enabled name %d differs from native declaration: %q, %s", i, enabled[i], raw)
		}
	}
}
