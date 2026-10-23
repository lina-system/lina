// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

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
