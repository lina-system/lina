// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/accounts"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/design"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestM2TUIUsageUnknownInstalledCore(t *testing.T) {
	var fixture struct {
		Labels, Commands []string
		Plan             string
	}
	raw, err := os.ReadFile("../m2/tui-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "")
	core := start(false)
	h.stopCore(core)
	raw, err = os.ReadFile(filepath.Join(h.root, "system/core/main.json"))
	if err != nil {
		t.Fatal(err)
	}
	var main struct {
		ID string `json:"main_id"`
	}
	if err = json.Unmarshal(raw, &main); err != nil {
		t.Fatal(err)
	}
	db, closeDB := installedAcceptanceStore(t, h)
	vault, err := secrets.OpenFiles(h.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	store, err := accounts.Open(h.ctx, db, vault, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	for i, label := range fixture.Labels {
		id := provider.AccountID(fmt.Sprintf("hidden-fixture-%d", i))
		a := accounts.Account{ID: id, Person: "person:" + main.ID, Provider: provider.ChatGPT, Label: label, Auth: provider.APIKey}
		if i == 0 {
			a.Plan = &accounts.PlanRef{Name: fixture.Plan, Source: "synthetic"}
		}
		if err = store.Add(h.ctx, a, accounts.Rotated{Tokens: secrets.NewTokens("synthetic-key", "")}); err != nil {
			t.Fatal(err)
		}
		if i == 1 {
			if err = store.SetStatus(h.ctx, id, accounts.UsageCapped); err != nil {
				t.Fatal(err)
			}
		}
	}
	store.Close()
	vault.Close()
	closeDB()
	core = start(false)
	defer h.stopCore(core)
	for _, width := range []int{100, 40} {
		terminal := h.startTerminalSize([]string{"TERM=xterm-256color", "LC_ALL=C"}, false, true, width, 24)
		waitComposerReady(t, h, terminal)
		for _, command := range fixture.Commands {
			h.keys(terminal, command+"\r")
			view := screenText(waitScreen(t, h, terminal, design.Text(design.English, "usage.unknown")))
			if strings.Count(view, design.Text(design.English, "usage.unknown")) != len(fixture.Labels) || strings.Contains(view, "hidden-fixture-") || strings.Contains(view, "0%") {
				t.Fatalf("unreported quotas are not unknown: %s", view)
			}
			if width == 100 && !strings.Contains(view, fixture.Plan) {
				t.Fatal("reported plan missing")
			}
			h.keys(terminal, "\x1b")
			waitComposerReady(t, h, terminal)
		}
		h.keys(terminal, "\x04")
		terminal.process.wait(t, 0)
		h.expectTerminalRestored(terminal)
	}
}
