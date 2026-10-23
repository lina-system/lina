// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/internal/fakecore"
)

func TestM1TUIShell(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("tui-shell.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Connect [][]fakecore.Step `json:"connect"`
		Refuse  [][]fakecore.Step `json:"refuse"`
		Locales []string          `json:"locales"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	for _, locale := range scenario.Locales {
		t.Run(locale, func(t *testing.T) {
			language := design.LanguageFor(locale)
			for _, test := range []struct {
				name, state string
				connections [][]fakecore.Step
				exit        int
			}{
				{"connect", "live", scenario.Connect, 0},
				{"refuse", "refused", scenario.Refuse, 78},
				{"absent", "stopped", nil, 69},
			} {
				t.Run(test.name, func(t *testing.T) {
					h := newHarness(t, binary)
					var done <-chan error
					if test.connections != nil {
						done = h.fakeCoreConnections(true, test.connections...)
					}
					term := h.startTUI("TERM=xterm-256color", "NO_COLOR=1", "LC_ALL="+locale)
					expectCatalogState(t, h, term, language, "connecting")
					expectCatalogState(t, h, term, language, test.state)
					if test.exit == 0 {
						h.keys(term, "q\n")
						expectCatalogState(t, h, term, language, "quit")
					}
					term.process.wait(t, test.exit)
					for range term.lines {
					} // Join the terminal reader and consume its final action.
					if done != nil {
						if err := <-done; err != nil {
							t.Fatal(err)
						}
					} else {
						entries, err := os.ReadDir(h.root)
						if err != nil || len(entries) != 0 {
							t.Fatalf("absent Core created state: %v", err)
						}
					}
				})
			}
		})
	}
}

// Resolve the asserted state from its id. Translations may change freely;
// this verifies that the real terminal displays the selected catalog entry.
func expectCatalogState(t *testing.T, h *harness, terminal *terminal, language design.Language, id string) {
	t.Helper()
	label, ok := design.Lookup(language, id)
	if !ok {
		t.Fatalf("missing catalog id %s", id)
	}
	for {
		select {
		case line, ok := <-terminal.lines:
			if !ok {
				t.Fatalf("terminal closed before state %s", id)
			}
			if strings.ContainsRune(line, '\x1b') {
				t.Fatal("NO_COLOR emitted escapes")
			}
			if strings.HasSuffix(strings.TrimSpace(line), label) {
				return
			}
		case <-h.ctx.Done():
			t.Fatalf("terminal did not display state %s", id)
		}
	}
}
