// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/persona"
)

// TestM1TUIIdentityFromAgentStore installs a synthetic named agent with four
// distinct persona layers, types "who are you" through the real TUI, and checks
// the provider stub's request for that stored identity, owner and persona, plus
// the exact enabled tool list. It checks request assembly, not model compliance
// with the persona. The enabled list must match the tools declared in the
// provider request.
func TestM1TUIIdentityFromAgentStore(t *testing.T) {
	t.Parallel()

	for _, defaultAgent := range []bool{false, true} {
		t.Run(fmt.Sprint(defaultAgent), func(t *testing.T) {
			var want agents.Preparation
			var owner string
			requests := make(chan struct{}, 1)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Instructions string
					Input        []json.RawMessage
					Tools        []json.RawMessage
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				if !strings.Contains(string(mustJSON(t, body.Input)), "who are you") {
					t.Error("TUI identity question did not reach the provider")
				}
				var persona struct {
					AgentID       string        `json:"agent_id"`
					Name          string        `json:"name"`
					OwnerPersonID string        `json:"owner_person_id"`
					Layers        agents.Layers `json:"layers"`
				}
				if err := json.Unmarshal(instructionBlock(body.Instructions, "persona"), &persona); err != nil {
					t.Errorf("missing persona block in provider instructions: %v", err)
				} else if persona.AgentID != want.AgentID || persona.Name != want.Name || persona.OwnerPersonID != owner || persona.Layers != want.Layers {
					t.Errorf("provider persona differs from the active agent store: %+v", persona)
				}
				var enabled []json.RawMessage
				if err := json.Unmarshal(instructionBlock(body.Instructions, "enabled_tools"), &enabled); err != nil {
					t.Errorf("missing enabled tool list in provider instructions: %v", err)
				} else if !reflect.DeepEqual(mustJSON(t, enabled), mustJSON(t, append([]json.RawMessage{}, body.Tools...))) {
					t.Error("instructions advertise tools different from this request's declarations")
				}
				for _, vendor := range []string{"anthropic", "claude", "openai", "sdk"} {
					if strings.Contains(strings.ToLower(body.Instructions), vendor) {
						t.Errorf("identity instructions name a model vendor/runtime: %s", vendor)
					}
				}
				requests <- struct{}{}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Identity fixture received.\"}]}}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			}))
			defer provider.Close()
			h, start := installedConversationHarness(t, provider.URL)
			want, owner = seedConversationPersona(t, h, defaultAgent)
			core := start(true)
			terminal, _ := h.startConversationTUI(80, 24, "TERM=xterm-256color")
			waitComposerReady(t, h, terminal)
			h.keys(terminal, "who are you\r")
			waitScreen(t, h, terminal, "Identity fixture received.")
			select {
			case <-requests:
			case <-h.ctx.Done():
				t.Fatal("provider did not receive the TUI turn")
			}
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.expectTerminalRestored(terminal)
			h.stopCore(core)
		})
	}
}

func instructionBlock(instructions, name string) []byte {
	_, rest, _ := strings.Cut(instructions, "<"+name+">\n")
	body, _, _ := strings.Cut(rest, "\n</"+name+">")
	return []byte(body)
}

// Seed synthetic canon before Core starts, using the same stores as production.
func seedConversationPersona(t *testing.T, h *harness, defaultAgent bool) (agents.Preparation, string) {
	t.Helper()
	controls, err := ctlfiles.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	defer controls.Close()
	if err = controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	raw, err := controls.Read("main.json")
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	owner := "person:" + main.ID
	db, err := canon.Open(h.ctx, canon.Config{Path: filepath.Join(h.root, "canon/canon.sqlite"), Epoch: 1, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	store, err := agents.Open(h.root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var agent agents.Agent
	if defaultAgent {
		core, voice, err := persona.Load(persona.Shipped(), "lina")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpdatePresetLayers(h.ctx, "lina", core, voice, owner); err != nil {
			t.Fatal(err)
		}
		first, err := store.FirstRun(h.ctx, owner, true)
		if err != nil {
			t.Fatal(err)
		}
		agent = first.Agent
	} else {
		agent, err = store.Create(h.ctx, agents.Create{Name: "Aster", PersonID: owner, Layers: agents.Layers{
			Core: "Synthetic companion identity.", DefaultVoice: "Speak calmly.", UserVoice: "Use short sentences.", GrowthSnapshot: "Explain uncertainty plainly.",
		}})
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Write(h.ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(h.ctx, "INSERT INTO agent_first_run VALUES(1,?)", agent.ID)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := store.Prepare(h.ctx, "fixture", agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, owner
}
