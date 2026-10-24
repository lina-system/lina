// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

func TestMessengerAddonMissingConfigurationReportsItsCause(t *testing.T) {
	t.Setenv("LINA_MESSENGER_NODE", "")
	t.Setenv("LINA_MESSENGER_ADDON", "")
	manager := messengerAddon(t.TempDir(), "")
	defer manager.Close(context.Background())
	a, err := manager.Addon("discord", "account")
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.SignIn(t.Context(), messenger.SourceID{Platform: "discord", Account: "account"})
	if !errors.Is(err, addon.ErrNotConfigured) {
		t.Fatalf("missing runtime: %v", err)
	}
}
