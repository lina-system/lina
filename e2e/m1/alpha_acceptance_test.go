// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func alphaConversationScreen(t *testing.T, h *harness, terminal *terminal, language design.Language) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	// Writing Escape to the PTY does not prove the overlay has closed. Use
	// the returned conversation frame for panel assertions, not a later read.
	snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool {
		rows := screenRows(s)
		return s.AltScreen && !s.CursorHidden && len(rows) > 0 &&
			strings.Contains(rows[len(rows)-1], design.Text(language, "live")) &&
			strings.Contains(rows[len(rows)-1], "? "+design.Text(language, "shortcuts.title"))
	})
	if err != nil {
		t.Fatalf("alpha conversation did not return: %v; cells=%s", err, screenText(snapshot))
	}
	return snapshot
}

// T-16 uses minutes, then hours/minutes, then days/hours. The wait for a
// person or model is unbounded by those display units; do not require "0m".
var alphaWorkTitle = regexp.MustCompile(`^.+ +(?:[0-9]m|[1-5][0-9]m|(?:[1-9]|1[0-9]|2[0-3])h [0-5][0-9]m|[1-9][0-9]*d (?:[01][0-9]|2[0-3])h)$`)

func alphaHasWorkCard(panel string) bool {
	rows := strings.Split(panel, "\n")
	for i := 0; i+1 < len(rows); i++ {
		if alphaWorkTitle.MatchString(strings.TrimSpace(rows[i])) &&
			strings.HasPrefix(rows[i+1], "  ") && strings.TrimSpace(rows[i+1]) != "" {
			return true
		}
	}
	return false
}

func alphaScreen(t *testing.T, h *harness, terminal *terminal, stage string, ready func(string) bool, forbidden string) tuiterm.Snapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
	defer cancel()
	snapshot, err := terminal.screen.Wait(ctx, func(s tuiterm.Snapshot) bool { return ready(screenText(s)) })
	if err != nil {
		t.Fatalf("alpha screen %s did not arrive: %v", stage, err)
	}
	t.Log("observed screen:", stage)
	text, raw := screenText(snapshot), terminal.screen.Bytes()
	if forbidden != "" && (strings.Contains(text, forbidden) || bytes.Contains(raw, []byte(forbidden))) {
		t.Fatalf("alpha screen %s exposed the synthetic credential", stage)
	}
	if directory := os.Getenv("LINA_ALPHA_CAPTURES"); directory != "" {
		directory = filepath.Join(directory, t.Name())
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		frame := mustJSON(t, snapshot)
		for name, data := range map[string][]byte{stage + ".txt": []byte(text), stage + ".ansi": raw, stage + ".json": frame} {
			if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return snapshot
}

func alphaTurn(t *testing.T, h *harness, terminal *terminal, observer *clientapi.Client, input, reply, stage string) {
	t.Helper()
	h.keys(terminal, "\x1b[200~"+input+"\x1b[201~\r")
	for {
		frame, err := observer.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Error != nil {
			t.Fatal("alpha input refused:", frame.Error.Code)
		}
		if state, ok := frame.Payload.(protocol.TurnState); ok && state.Active != nil && !*state.Active {
			break
		}
	}
	alphaScreen(t, h, terminal, stage, func(s string) bool { return strings.Contains(s, reply) }, "")
}
