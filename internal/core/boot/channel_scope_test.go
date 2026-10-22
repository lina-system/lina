// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"errors"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/channels"
)

func TestChannelRevalidatesOriginalThreadAddress(t *testing.T) {
	for _, change := range []string{"disable-tools", "change-agent"} {
		t.Run(change, func(t *testing.T) {
			r, _, agent, other := managementFixture(t)
			ctx := t.Context()
			store := channels.New(r.db)
			bot := channels.Bot{Platform: "discord", Account: "fixture"}
			now := time.Now()
			if err := store.Connect(ctx, bot, r.person); err != nil {
				t.Fatal(err)
			}
			code, err := store.PairingCode(ctx, bot, r.person, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Pair(ctx, bot, code, "42", "100", now); err != nil {
				t.Fatal(err)
			}
			parent := channels.Binding{Address: channels.Address{Bot: bot, Chat: "parent"}, Agent: agent, Person: r.person, Conversation: "group", Scope: channels.Scope{Reads: channels.Addressed, Writes: channels.ReplyOnly, Tools: true, Memory: true}}
			if err := store.Bind(ctx, parent); err != nil {
				t.Fatal(err)
			}
			address := parent.Address
			address.Topic = "thread"
			binding, err := store.Binding(ctx, address)
			if err != nil || binding != parent {
				t.Fatalf("fallback binding: %+v %v", binding, err)
			}
			r.channelBinding = &binding
			r.channelAddress = address
			if err := r.validateChannel(ctx); err != nil {
				t.Fatalf("unchanged fallback refused: %v", err)
			}
			replacement := parent
			replacement.Address = address
			if change == "disable-tools" {
				replacement.Scope.Tools = false
			} else {
				replacement.Agent = other
			}
			// Another thread must not revoke the current thread's unchanged scope.
			sibling := replacement
			sibling.Topic = "sibling"
			if err := store.Bind(ctx, sibling); err != nil {
				t.Fatal(err)
			}
			if err := r.validateChannel(ctx); err != nil {
				t.Fatalf("sibling changed this turn: %v", err)
			}
			if err := store.Bind(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			if err := r.validateChannel(ctx); !errors.Is(err, channels.ErrDenied) {
				t.Fatalf("superseded parent scope remained valid: %v", err)
			}
			if err := store.Unbind(ctx, address, r.person); err != nil {
				t.Fatal(err)
			}
			if err := r.validateChannel(ctx); err != nil {
				t.Fatalf("restored parent refused: %v", err)
			}
		})
	}
}
