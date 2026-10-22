// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
)

func personaFixture(t *testing.T, core, voice string) string {
	t.Helper()
	source := t.TempDir()
	dir := filepath.Join(source, "characters/lina/persona")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"core-v1.md": core, "default-voice-v1.md": voice} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return source
}

func setPersonaSource(t *testing.T, root, source string) {
	t.Helper()
	data := mustJSON(t, map[string]string{"persona_source": source})
	if err := os.WriteFile(filepath.Join(root, "system/core/config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestM1PersonaSource(t *testing.T) {
	t.Parallel()

	for _, mode := range []string{"configured", "unconfigured", "missing voice", "missing core", "oversize core", "oversize combined", "oversize encoded"} {
		t.Run(mode, func(t *testing.T) {
			requests := make(chan string, 8)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Instructions string }
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					return
				}
				requests <- body.Instructions
				emitResponse(w, "fixture reply", true)
			}))
			defer server.Close()
			coreText, voiceText := "fixture-core-A", "fixture-voice-A"
			switch mode {
			case "oversize core":
				coreText = strings.Repeat("x", 129000)
			case "oversize combined":
				coreText, voiceText = strings.Repeat("x", 40000), strings.Repeat("y", 40000)
			case "oversize encoded":
				coreText = strings.Repeat("<", 25000)
			}
			source := personaFixture(t, coreText, voiceText)
			if strings.HasPrefix(mode, "missing ") {
				name := "default-voice-v1.md"
				if mode == "missing core" {
					name = "core-v1.md"
				}
				if err := os.Remove(filepath.Join(source, "characters/lina/persona", name)); err != nil {
					t.Fatal(err)
				}
			}
			var installArgs []string
			if mode != "unconfigured" {
				installArgs = []string{"--persona-source", source}
			}
			h, start := installedConversationHarness(t, server.URL, installArgs...)
			if mode == "unconfigured" {
				// Older installs have no Core config file at all.
				if err := os.Remove(filepath.Join(h.root, "system/core/config.json")); err != nil {
					t.Fatal(err)
				}
			}
			core := start(true)
			c, conversation := conversationClient(t, h)
			request := func(key string) string {
				admittedTurn(t, h, c, conversation, key, "who are you")
				select {
				case instructions := <-requests:
					return instructions
				case <-h.ctx.Done():
					t.Fatal("no provider request")
					return ""
				}
			}
			check := func(instructions, wantCore, wantVoice string) {
				t.Helper()
				if !strings.Contains(instructions, c.Welcome.Agents[0].Name) || strings.Contains(strings.ToLower(instructions), "placeholder") {
					t.Errorf("request lacks the agent name or includes a placeholder: %q", instructions)
				}
				var identity struct {
					Name   string        `json:"name"`
					Layers agents.Layers `json:"layers"`
				}
				if err := json.Unmarshal(instructionBlock(instructions, "persona"), &identity); err != nil {
					t.Fatalf("missing prepared identity: %v", err)
				}
				if identity.Name != c.Welcome.Agents[0].Name || identity.Layers != (agents.Layers{Core: wantCore, DefaultVoice: wantVoice}) {
					t.Errorf("unexpected persona identity or layers: %+v", identity)
				}
				if wantCore != "" && (strings.Count(instructions, wantCore) != 1 || strings.Count(instructions, wantVoice) != 1) {
					t.Errorf("fixture layers missing or duplicated: %q", instructions)
				}
			}
			for _, key := range []string{"first", "second"} {
				wantCore, wantVoice := "", ""
				if mode == "configured" {
					wantCore, wantVoice = "fixture-core-A", "fixture-voice-A"
				}
				check(request(key), wantCore, wantVoice)
			}
			// The source is reread at the next turn, without restarting Core.
			updatedSource := personaFixture(t, "fixture-core-B", "fixture-voice-B")
			setPersonaSource(t, h.root, updatedSource)
			updated := request("changed-source")
			check(updated, "fixture-core-B", "fixture-voice-B")
			if strings.Contains(updated, "fixture-core-A") || strings.Contains(updated, "fixture-voice-A") {
				t.Error("old source survived a config change")
			}
			if err := os.WriteFile(filepath.Join(updatedSource, "characters/lina/persona/core-v1.md"), []byte("fixture-core-C"), 0600); err != nil {
				t.Fatal(err)
			}
			check(request("edited-source"), "fixture-core-C", "fixture-voice-B")
			if mode != "configured" {
				setPersonaSource(t, h.root, "")
				check(request("removed-source"), "", "")
			}
			c.Close()
			h.stopCore(core)
			warnings := strings.Count(core.stderr.String(), "Lina persona")
			if mode == "configured" && warnings != 0 || mode != "configured" && warnings != 1 {
				t.Errorf("setup warnings = %d, stderr: %s", warnings, core.stderr.String())
			}
		})
	}
}

func TestM1InstallPersonaSource(t *testing.T) {
	t.Parallel()

	source := personaFixture(t, "fixture-install-core", "fixture-install-voice")
	h := newHarness(t, buildLina(t))
	bundle := makeInstallBundle(t, h.root, h.binary, buildCommand(t, "lina-node"))
	installConversationBundle(t, h, bundle, "", "--persona-source", source)
	data, err := os.ReadFile(filepath.Join(h.root, "system/core/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		PersonaSource string `json:"persona_source"`
	}
	if err := json.Unmarshal(data, &config); err != nil || config.PersonaSource != source {
		t.Fatalf("source not saved in Core config: %s, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(h.root, "system/core/config.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("config is not private: %v", err)
	}
}
