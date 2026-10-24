// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"testing"

	"github.com/thisisjun786/lina/internal/core/messenger"
	"github.com/thisisjun786/lina/internal/core/messenger/addon"
)

func TestMessengerAddonResolvesEachSourceAccount(t *testing.T) {
	manager := addon.NewManager(addon.Config{StateRoot: t.TempDir()})
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	resolve := resolveMessenger(&MessengerConfig{Addon: manager})
	for _, account := range []string{"first", "second"} {
		source := messenger.Source{Platform: "discord", Account: account, Mode: messenger.ModeUnofficial, Adapter: "messenger-addon"}
		a, ok := resolve(source)
		if !ok {
			t.Fatal("account not resolved")
		}
		_, err := a.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: account})
		if err != addon.ErrNotConfigured {
			t.Fatalf("correct account: %v", err)
		}
		_, err = a.ListChats(t.Context(), messenger.SourceID{Platform: "discord", Account: "another"})
		if err != messenger.ErrInvalid {
			t.Fatalf("wrong account: %v", err)
		}
	}
	for _, source := range []messenger.Source{
		{Platform: "discord", Account: "first", Mode: messenger.ModeOfficial, Adapter: "discord-bot"},
		{Platform: "discord", Account: "first", Mode: messenger.ModeUnofficial, Adapter: "another-adapter"},
	} {
		if _, ok := resolve(source); ok {
			t.Fatal("unrelated adapter resolved to add-on")
		}
	}
}
