// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/lease"
)

func (f *adapterFixture) approval(t *testing.T, id string) authority.Approval {
	t.Helper()
	a := authority.Approval{ID: id, EffectKey: id, Person: "person", Agent: f.agent, Epoch: 7,
		State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny},
		What: "Send a message", Where: "destination", Arguments: json.RawMessage(`{"body":"hello"}`)}
	data, _ := json.Marshal(a)
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals VALUES(?,?,?,?)", a.ID, a.EffectKey, "fingerprint", data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return a
}

type rejectPresentationWrite struct{ channels.Canon }

func (rejectPresentationWrite) Write(context.Context, func(*canon.Tx) error) error {
	return errors.New(testToken)
}

func TestFailedPresentationCommitCannotEnableButtons(t *testing.T) {
	f := newAdapterFixture(t)
	f.approval(t, "request_a")
	sends := 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "sendMessage") {
			sends++
			return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
		}
		return response(r, 200, `{"ok":true,"result":true}`), nil
	}))
	a.db = rejectPresentationWrite{Canon: f.db}
	l := lease.New(func() {})
	defer l.Release()
	result, err := a.PresentApproval(t.Context(), l, f.address, "request_a")
	if !errors.Is(err, errAdmission) || result.Outcome != gateway.Uncertain || sends != 1 {
		t.Fatalf("failed persistence: %+v %v sends=%d", result, err, sends)
	}
	a.db = f.db
	q := gotgbot.CallbackQuery{Id: "callback", From: gotgbot.User{Id: 100}, Data: "request_a:o", Message: gotgbot.Message{
		MessageId: 19, Date: 1, Chat: gotgbot.Chat{Id: 100, Type: "private"}, From: &gotgbot.User{Id: 123456, IsBot: true},
	}}
	if err := a.admitButton(t.Context(), q, "10", "", "11"); err != nil {
		t.Fatal(err)
	}
	stored, err := f.approvals.Approval(t.Context(), "request_a")
	if err != nil || stored.State != authority.Pending || sends != 1 {
		t.Fatalf("uncommitted presentation authorized or resent: %s %v sends=%d", stored.State, err, sends)
	}
}

func TestApprovalButtonMustMatchPresentedMessage(t *testing.T) {
	for _, tc := range []struct {
		name, request  string
		message, topic int64
		accepted       bool
	}{
		{"matching", "request_a", 19, 0, true},
		{"other request", "request_b", 19, 0, false},
		{"other message", "request_a", 20, 0, false},
		{"other topic", "request_a", 19, 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdapterFixture(t)
			f.approval(t, "request_a")
			f.approval(t, "request_b")
			edits := 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				io.Copy(io.Discard, r.Body)
				if strings.HasSuffix(r.URL.Path, "sendMessage") {
					return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
				}
				if strings.HasSuffix(r.URL.Path, "editMessageText") {
					edits++
				}
				return response(r, 200, `{"ok":true,"result":true}`), nil
			})
			a := f.adapter(t, transport)
			l := lease.New(func() {})
			defer l.Release()
			result, err := a.PresentApproval(t.Context(), l, f.address, "request_a")
			if err != nil || result.Outcome != gateway.Delivered {
				t.Fatalf("present: %+v %v", result, err)
			}
			// A fresh adapter proves that the binding is durable, not a local cache.
			a = f.adapter(t, transport)
			q := gotgbot.CallbackQuery{Id: "callback", From: gotgbot.User{Id: 100}, Data: tc.request + ":o", Message: gotgbot.Message{
				MessageId: tc.message, MessageThreadId: tc.topic, Date: 1, Chat: gotgbot.Chat{Id: 100, Type: "private"}, From: &gotgbot.User{Id: 123456, IsBot: true},
			}}
			if err := a.admitButton(t.Context(), q, "10", "", "11"); err != nil {
				t.Fatal(err)
			}
			stored, err := f.approvals.Approval(t.Context(), tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.accepted {
				if stored.State != authority.Approved || edits != 1 {
					t.Fatalf("matching presentation refused: %s edits=%d", stored.State, edits)
				}
			} else if stored.State != authority.Pending || edits != 0 {
				t.Fatalf("mismatched presentation changed approval: %s edits=%d", stored.State, edits)
			}
		})
	}
}
