// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

func TestRestoreInvalidatesChannelDeliveries(t *testing.T) {
	for _, targetKind := range []string{"same", "other", "resumed"} {
		t.Run(targetKind, func(t *testing.T) {
			cfg, assets := fixture(t)
			personas, err := agents.Open(cfg.Root, cfg.Canon)
			if err != nil {
				t.Fatal(err)
			}
			defer personas.Close()
			first, err := personas.FirstRun(t.Context(), "person-test", true)
			if err != nil {
				t.Fatal(err)
			}
			address := channels.Address{Bot: channels.Bot{Platform: "telegram", Account: "fixture"}, Chat: "123"}
			now := time.Unix(100, 0)
			queue := channels.New(cfg.Canon)
			if err := queue.Connect(t.Context(), address.Bot, "person-test"); err != nil {
				t.Fatal(err)
			}
			code, err := queue.PairingCode(t.Context(), address.Bot, "person-test", now)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.Pair(t.Context(), address.Bot, code, "123", address.Chat, now); err != nil {
				t.Fatal(err)
			}
			if err := queue.Bind(t.Context(), channels.Binding{Address: address, Agent: first.Agent.ID, Conversation: "conversation", Person: "person-test", Scope: channels.Scope{Reads: channels.DMOnly, Writes: channels.MayStart}}); err != nil {
				t.Fatal(err)
			}
			_, err = ledger.New(cfg.Canon, nil).Admit(t.Context(), guard.EntryAgent, ledger.Input{ID: "reply", ConversationID: "conversation", AgentID: first.Agent.ID, Surface: ledger.Surface{Kind: "channel", Platform: address.Platform, ChatID: address.Chat}, OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: "synthetic reply"})
			if err != nil {
				t.Fatal(err)
			}
			states := []channels.State{channels.Queued, channels.Sending, channels.Sent, channels.Unknown, channels.Blocked, channels.Refused}
			captured := make(map[channels.State]channels.Message)
			for _, state := range states {
				id := string(state)
				if err := queue.Enqueue(t.Context(), channels.Message{ID: id, Address: address, Agent: first.Agent.ID, LedgerRef: "reply", Parts: []string{"first part", "second part"}}); err != nil {
					t.Fatal(err)
				}
				// Include a delivered first part and a deferred retry in the snapshot.
				if err := cfg.Canon.Write(t.Context(), func(tx *canon.Tx) error {
					_, err := tx.ExecContext(t.Context(), `UPDATE channel_outbound SET state=?,next_part=1,attempt=2,retry_at=?,message_ids='["remote-first"]' WHERE id=?`, state, now.Add(time.Minute).UnixNano(), id)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				captured[state], err = queue.Message(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
			}
			m, err := Create(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			personas.Close()
			assets.Close()
			cfg.Canon.Close(context.Background())
			cfg.Controls.(*ctlfiles.Store).Close()
			target := cfg.Root
			if targetKind == "other" {
				target = filepath.Join(t.TempDir(), "restored")
			}
			restoreTarget(t, target, "45")
			options := RestoreConfig{Root: target, Generation: filepath.Join(cfg.Root, "backups", m.GenerationID), Migrations: canon.Migrations(), Artifacts: cfg.Artifacts}
			if targetKind == "resumed" {
				fault := errors.New("interrupted publication")
				options.boundary = func(at string) error {
					if at == "installed:canon" {
						return fault
					}
					return nil
				}
				if _, err := Restore(t.Context(), options); !errors.Is(err, fault) {
					t.Fatalf("interruption: %v", err)
				}
				options.boundary = nil
			}
			if _, err := Restore(t.Context(), options); err != nil {
				t.Fatal(err)
			}
			ctl, err := ctlfiles.Open(target)
			if err != nil {
				t.Fatal(err)
			}
			defer ctl.Close()
			if err := ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(target, "canon/canon.sqlite"), Epoch: 46, Controls: ctl, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close(context.Background())
			queue = channels.New(db)
			for _, state := range states {
				got, err := queue.Message(t.Context(), string(state))
				if err != nil {
					t.Fatal(err)
				}
				want := captured[state]
				if state == channels.Queued || state == channels.Sending {
					want.State = channels.Unknown
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("restored %s delivery: got %+v, want %+v", state, got, want)
				}
			}
			if err := queue.Settle(t.Context(), captured[channels.Sending], channels.Sent, "stale-receipt", time.Time{}); !errors.Is(err, channels.ErrConflict) {
				t.Errorf("restored claim accepted a stale completion: %v", err)
			}
			if m, err := queue.ClaimNext(t.Context(), address, now.Add(time.Hour)); !errors.Is(err, channels.ErrEmpty) {
				t.Errorf("restored delivery can be dispatched again: %+v %v", m, err)
			}
		})
	}
}
