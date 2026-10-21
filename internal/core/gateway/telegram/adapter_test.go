// SPDX-License-Identifier: Apache-2.0

package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

type adapterFixture struct {
	db        *canon.Store
	channels  *channels.Store
	approvals *authority.Service
	bot       channels.Bot
	address   channels.Address
	agent     string
}

func newAdapterFixture(t *testing.T) *adapterFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err := ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	ms := canon.Migrations()
	ms = append(ms, canon.Migration{Version: len(ms) + 1, SQL: channels.Schema}, canon.Migration{Version: len(ms) + 2, SQL: Schema})
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 7, Controls: ctl, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	as, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { as.Close() })
	agent, err := as.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "person", Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	f := &adapterFixture{db: db, channels: channels.New(db), bot: channels.Bot{Platform: "telegram", Account: "123456"}, agent: agent.ID}
	f.address = channels.Address{Bot: f.bot, Chat: "100"}
	now := time.Now()
	if err := f.channels.Connect(t.Context(), f.bot, "person"); err != nil {
		t.Fatal(err)
	}
	code, err := f.channels.PairingCode(t.Context(), f.bot, "person", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.channels.Pair(t.Context(), f.bot, code, "100", "100", now); err != nil {
		t.Fatal(err)
	}
	if err := f.channels.Bind(t.Context(), channels.Binding{Address: f.address, Agent: f.agent, Person: "person", Conversation: "conversation", Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	f.approvals, err = authority.Open(t.Context(), db, authority.Config{Epoch: 7, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("unused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *adapterFixture) adapter(t *testing.T, transport http.RoundTripper) *Adapter {
	t.Helper()
	a, err := New(Config{Bot: f.bot, Token: secret.New(testToken), DB: f.db, Epoch: 7, Approvals: f.approvals, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAdapterUsesDurableGatewayAndOwnerButtons(t *testing.T) {
	f := newAdapterFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	approval := authority.Approval{ID: "request_123", EffectKey: "effect", Person: "person", Agent: f.agent, Epoch: 7, State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny}, Arguments: json.RawMessage(`{}`)}
	data, _ := json.Marshal(approval)
	if err := f.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO authority_approvals VALUES(?,?,?,?)", approval.ID, approval.EffectKey, "fingerprint", data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	polls, turns, edits := 0, 0, 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		io.Copy(io.Discard, r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "getUpdates"):
			polls++
			if polls == 1 {
				return response(r, 200, `{"ok":true,"result":[
			 {"update_id":10,"message":{"message_id":11,"date":1,"chat":{"id":100,"type":"private"},"from":{"id":100},"text":"hello `+testToken+`"}},
			 {"update_id":11,"callback_query":{"id":"outsider","from":{"id":200},"data":"request_123:o","message":{"message_id":19,"date":1,"chat":{"id":100,"type":"private"}}}}
			]}`), nil
			}
			if polls == 2 {
				stored, err := f.approvals.Approval(ctx, approval.ID)
				if err != nil || stored.State != authority.Pending || edits != 0 {
					t.Fatal("outsider changed approval or buttons")
				}
				return response(r, 200, `{"ok":true,"result":[{"update_id":12,"callback_query":{"id":"owner","from":{"id":100},"data":"request_123:o","message":{"message_id":19,"date":1,"chat":{"id":100,"type":"private"}}}}]}`), nil
			}
			cancel()
			return nil, ctx.Err()
		case strings.HasSuffix(r.URL.Path, "editMessageText"):
			edits++
			return response(r, 200, `{"ok":true,"result":{"message_id":19}}`), nil
		default:
			return response(r, 200, `{"ok":true,"result":true}`), nil
		}
	}))
	ledgerStore := ledger.New(f.db, nil)
	g := gateway.New(f.db, func(ctx context.Context, d gateway.Delivery) error {
		turns++
		record, err := inbox.New(f.db).Get(ctx, inbox.Key{Conversation: d.Conversation, Source: f.bot.Key(), NativeID: d.NativeID})
		if err != nil {
			return err
		}
		var event gateway.Inbound
		if err := json.Unmarshal(record.Body, &event); err != nil {
			return err
		}
		var message Message
		if err := json.Unmarshal(event.Body, &message); err != nil {
			return err
		}
		now := time.Now().UTC()
		_, err = ledgerStore.Admit(ctx, guard.EntryOwner, ledger.Input{
			ID: "telegram-input", ConversationID: d.Conversation, PersonID: d.Person, AgentID: d.Agent,
			Surface:    ledger.Surface{Kind: "channel", Platform: "telegram", ChatID: event.Chat},
			OccurredAt: now, RecordedAt: now, TimeQuality: "recorded", Body: message.Text,
		})
		return err
	})
	if err := a.Run(ctx, g); !errors.Is(err, context.Canceled) {
		t.Fatalf("run=%v", err)
	}
	stored, err := f.approvals.Approval(t.Context(), approval.ID)
	if err != nil || stored.State != authority.Approved || turns != 1 || edits != 1 {
		t.Fatalf("state=%s turns=%d edits=%d err=%v", stored.State, turns, edits, err)
	}
	cursor, err := f.channels.Cursor(t.Context(), f.bot)
	if err != nil || cursor != "13" {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}
	entry, err := ledgerStore.Get(t.Context(), "telegram-input")
	if err != nil || entry.Body.Text() != "hello "+secret.Placeholder {
		t.Fatalf("ledger did not retain only masked input: %v", err)
	}
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(t.Context(), "SELECT body FROM inbox")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var body []byte
			if err := rows.Scan(&body); err != nil {
				return err
			}
			if strings.Contains(string(body), testToken) {
				t.Fatal("token persisted in inbox")
			}
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterSendRequiresLeaseAndReportsPlatformOutcome(t *testing.T) {
	f := newAdapterFixture(t)
	calls := 0
	a := f.adapter(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		io.Copy(io.Discard, r.Body)
		return response(r, 429, `{"ok":false,"error_code":429,"parameters":{"retry_after":8}}`), nil
	}))
	o := gateway.Outbound{Address: f.address, Text: "hello"}
	if _, err := a.Send(t.Context(), lease.Lease{}, o); !errors.Is(err, lease.ErrInvalid) || calls != 0 {
		t.Fatalf("invalid lease: %v calls=%d", err, calls)
	}
	l := lease.New(func() {})
	defer l.Release()
	result, err := a.Send(t.Context(), l, o)
	if err != nil || result.Outcome != gateway.RetryLater || result.RetryAfter != 8*time.Second || calls != 1 {
		t.Fatalf("send=%+v %v", result, err)
	}
}
