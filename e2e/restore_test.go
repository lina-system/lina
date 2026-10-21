// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type restoreScenario struct {
	Targets          []string   `json:"targets"`
	InvalidArguments [][]string `json:"invalid_arguments"`
}

func readRestoreScenario(t *testing.T) restoreScenario {
	t.Helper()
	data, err := os.ReadFile("m1/restore.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario restoreScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

func TestRestoreCommandBoundaries(t *testing.T) {
	scenario := readRestoreScenario(t)
	binary := buildLina(t)
	h := newHarness(t, binary)
	help := h.command("restore", "--help")
	help.start(t)
	help.wait(t, 0)
	for _, args := range scenario.InvalidArguments {
		p := h.command(append([]string{"restore"}, args...)...)
		p.start(t)
		p.wait(t, 64)
		if !strings.Contains(p.stderr.String(), "lina restore") {
			t.Fatal("missing restore argument help")
		}
	}
	if err := os.MkdirAll(filepath.Join(h.root, "staging/restore.pending"), 0700); err != nil {
		t.Fatal(err)
	}
	core := h.startCore()
	core.wait(t, 78)
	if !strings.Contains(core.stderr.String(), "restore incomplete") {
		t.Fatalf("wrong refusal: %s", &core.stderr)
	}
	for _, path := range []string{h.socket, filepath.Join(h.root, "system/core/epoch")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("incomplete restore started Core")
		}
	}
}
