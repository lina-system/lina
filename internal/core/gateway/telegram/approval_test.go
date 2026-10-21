// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/grant"
)

func approvalFixture(t *testing.T) (*authority.Service, authority.Approval) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	store, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 7, Controls: controls, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(context.Background()) })
	service, err := authority.Open(t.Context(), store, authority.Config{Epoch: 7, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("no effect dispatch in this fixture")
	}})
	if err != nil {
		t.Fatal(err)
	}
	a := authority.Approval{ID: "request_123", EffectKey: "effect_123", Epoch: 7, Person: "person", Agent: "agent", State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny}, What: "Send a message", Where: "destination", Arguments: json.RawMessage(`{"body":"hello"}`)}
	data, _ := json.Marshal(a)
	if err := store.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", a.ID, a.EffectKey, "fingerprint", data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return service, a
}

func TestOnlyCurrentPairedOwnerCanAnswer(t *testing.T) {
	for _, tc := range []struct {
		name           string
		sender         int64
		epoch          uint64
		payload, agent string
		revoked        bool
		want           authority.ApprovalState
	}{
		{"outsider", 200, 7, "request_123:o", "agent", false, authority.Pending},
		{"owner once", 100, 7, "request_123:o", "agent", false, authority.Approved},
		{"owner deny", 100, 7, "request_123:d", "agent", false, authority.Denied},
		{"stale epoch", 100, 8, "request_123:o", "agent", false, authority.Pending},
		{"wrong agent", 100, 7, "request_123:o", "another", false, authority.Pending},
		{"revoked pairing", 100, 7, "request_123:o", "agent", true, authority.Pending},
		{"always forbidden", 100, 7, "request_123:a", "agent", false, authority.Pending},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, before := approvalFixture(t)
			lookup := func(_ context.Context, user, chat, thread int64) (string, string, error) {
				if user != 100 || chat != 12 || thread != 3 || tc.revoked {
					return "", "", errors.New("unpaired")
				}
				return "person", tc.agent, nil
			}
			q := gotgbot.CallbackQuery{Id: "callback", From: gotgbot.User{Id: tc.sender}, Data: tc.payload, Message: gotgbot.Message{MessageId: 19, MessageThreadId: 3, Date: 1, Chat: gotgbot.Chat{Id: 12}}}
			result := decideButton(t.Context(), tc.epoch, q, lookup, s)
			after, err := s.Approval(t.Context(), before.ID)
			if err != nil || after.State != tc.want {
				t.Fatalf("state=%s err=%v", after.State, err)
			}
			if tc.want == authority.Pending {
				if !reflect.DeepEqual(before, after) || result.accepted {
					t.Fatal("refused press changed approval")
				}
				if tc.sender != 100 && result.remove {
					t.Fatal("outsider removed owner buttons")
				}
			} else {
				if !result.accepted || !result.remove || after.AnsweredBy != "person" || after.ChatID != "12" || after.MessageID != "19" {
					t.Fatal("missing authenticated decision provenance")
				}
				again := decideButton(t.Context(), tc.epoch, q, lookup, s)
				if again.accepted || !again.remove {
					t.Fatal("replayed decision was accepted")
				}
			}
		})
	}
}
