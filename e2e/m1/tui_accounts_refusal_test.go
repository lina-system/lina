// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/design"
	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1TUIAccountDiscoveryRefusalInstalledCore(t *testing.T) {
	t.Parallel()

	h, _ := installedConversationHarness(t, "")
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	bound, recoverCore, ready := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	rows := clientsrv.LocalCombinations()
	for i := range rows {
		rows[i].Capabilities = []protocol.Capability{clientsrv.AccountsCapability, clientsrv.SignInCapability}
		rows[i].Evidence = []string{"go test ./e2e/m1 -run TestM1TUIAccountDiscoveryRefusalInstalledCore"}
	}
	socket := h.socket
	go func() {
		done <- boot.Run(ctx, boot.Config{StateRoot: h.root, Socket: socket, Client: clientsrv.Identity{Supported: rows}, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.SocketsBound {
				close(bound)
				select {
				case <-recoverCore:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if step == boot.Ready {
				close(ready)
			}
			return nil
		}})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	})
	select {
	case <-bound:
	case <-h.ctx.Done():
		t.Fatal("Core did not bind its client socket")
	}
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startScreenTUI("TERM=xterm-256color", "SSH_CONNECTION=synthetic")
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			if e.Error.Code != protocol.ErrorUnavailable {
				t.Fatalf("startup refusal = %s", e.Error.Code)
			}
			break
		}
	}
	view := screenText(waitScreen(t, h, terminal, design.Text(design.English, "unavailable")))
	if strings.Contains(view, design.Text(design.English, "account.loading")) || strings.Contains(view, design.Text(design.English, "account.heading")) || !strings.Contains(view, "/accounts") {
		t.Fatalf("startup refusal hid the composer or retained loading:\n%s", view)
	}
	// Typing still belongs to the composer, not a silently opened account block.
	h.keys(terminal, "retained draft")
	waitScreen(t, h, terminal, "retained draft")
	close(recoverCore)
	select {
	case <-ready:
	case <-h.ctx.Done():
		t.Fatal("Core did not complete recovery")
	}
	h.keys(terminal, "\x15/accounts\r")
	listed := tap.result(t, h, "list")
	if listed.Accounts == nil || len(*listed.Accounts) != 0 {
		t.Fatal("recovered Core did not return its empty account store")
	}
	waitScreen(t, h, terminal, design.Text(design.English, "account.help"))
	h.keys(terminal, "\x1b")
	snapshot, err := terminal.screen.Wait(h.ctx, func(snapshot tuiterm.Snapshot) bool {
		text := screenText(snapshot)
		return strings.Contains(text, "/signin") && strings.Contains(text, "/accounts") && !strings.Contains(text, design.Text(design.English, "account.heading"))
	})
	if err != nil {
		t.Fatal("account block did not close", err)
	}
	view = screenText(snapshot)
	if strings.Contains(view, design.Text(design.English, "unavailable")) || strings.Contains(view, design.Text(design.English, "account.loading")) {
		t.Fatalf("successful discovery retained startup failure:\n%s", view)
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.expectTerminalRestored(terminal)
}
