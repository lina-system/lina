// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/internal/core/gateway/telegram"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

var acceptanceBot = channels.Bot{Platform: "telegram", Account: "123456"}
var acceptanceChat = channels.Address{Bot: acceptanceBot, Chat: "100"}

func seedAcceptanceTelegram(t *testing.T, root string) string {
	db, close := acceptanceStore(t, root, 1)
	defer close()
	as, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer as.Close()
	a, err := as.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "person", Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	s := channels.New(db)
	if err := s.Connect(t.Context(), acceptanceBot, "person"); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	code, err := s.PairingCode(t.Context(), acceptanceBot, "person", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Pair(t.Context(), acceptanceBot, code, "100", "100", at); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(t.Context(), channels.Binding{Address: acceptanceChat, Agent: a.ID, Person: "person", Conversation: "conversation", Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	return a.ID
}

type telegramCommitBoundary struct {
	*canon.Store
	t *testing.T
}

func (db telegramCommitBoundary) Write(ctx context.Context, f func(*canon.Tx) error) error {
	return db.Store.Write(ctx, func(tx *canon.Tx) error {
		if err := f(tx); err != nil {
			return err
		}
		acknowledgeCrash(db.t)
		return nil
	})
}

const acceptanceTelegramMessage = `{"ok":true,"result":[{"update_id":10,"message":{"message_id":11,"date":1,"chat":{"id":100,"type":"private"},"from":{"id":100},"text":"synthetic input"}}]}`

func telegramWire(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// Serial: Telegram polling is guarded process-wide by the shared bot ID.
func TestM1TelegramCommitCrash(t *testing.T) {
	for _, boundary := range []string{"before_commit", "after_commit"} {
		t.Run(boundary, func(t *testing.T) {
			h := newHarness(t, "")
			agent := seedAcceptanceTelegram(t, h.root)
			killAcceptanceChild(t, h, "TestM1TelegramCommitChild", "LINA_M1_TELEGRAM_ROOT="+h.root, "LINA_M1_TELEGRAM_BOUNDARY="+boundary, "LINA_M1_TELEGRAM_AGENT="+agent)
			db, _ := acceptanceStore(t, h.root, 3)
			s := channels.New(db)
			cursor, err := s.Cursor(h.ctx, acceptanceBot)
			if err != nil {
				t.Fatal(err)
			}
			var rows int
			if err := db.Read(h.ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM inbox").Scan(&rows) }); err != nil {
				t.Fatal(err)
			}
			if boundary == "before_commit" {
				if rows != 0 || cursor != "" {
					t.Fatalf("partial admission: rows=%d cursor=%q", rows, cursor)
				}
			} else if rows != 1 || cursor != "11" {
				t.Fatalf("lost admission: rows=%d cursor=%q", rows, cursor)
			}
			if err := turn.New(db).Recover(h.ctx); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			polls, deliveries := 0, 0
			a, err := telegram.New(telegram.Config{Bot: acceptanceBot, Token: secret.New("123456:synthetic_acceptance_token"), DB: db, Epoch: 3, Transport: pairingTransport(func(*http.Request) (*http.Response, error) {
				polls++
				if polls <= 2 {
					return telegramWire(acceptanceTelegramMessage), nil
				}
				cancel()
				return nil, ctx.Err()
			})})
			if err != nil {
				t.Fatal(err)
			}
			g := gateway.New(db, func(ctx context.Context, d gateway.Delivery) error {
				deliveries++
				if d.Agent != agent || d.Person != "person" || d.Conversation != "conversation" {
					t.Fatalf("wrong route: %+v", d)
				}
				return turn.New(db).Claim(ctx, "new-turn", d.Conversation, d.InboxID)
			})
			if err := a.Run(ctx, g); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			want := 0
			if boundary == "before_commit" {
				want = 1
			}
			if deliveries != want {
				t.Fatalf("deliveries=%d want=%d", deliveries, want)
			}
			cursor, err = s.Cursor(h.ctx, acceptanceBot)
			if err != nil || cursor != "11" {
				t.Fatalf("replayed cursor=%q: %v", cursor, err)
			}
			if err := db.Read(h.ctx, func(tx *canon.Tx) error { return tx.QueryRowContext(h.ctx, "SELECT count(*) FROM inbox").Scan(&rows) }); err != nil || rows != 1 {
				t.Fatalf("replayed rows=%d: %v", rows, err)
			}
		})
	}
}

func TestM1TelegramCommitChild(t *testing.T) {
	root := os.Getenv("LINA_M1_TELEGRAM_ROOT")
	if root == "" {
		return
	}
	db, _ := acceptanceStore(t, root, 2)
	var storage channels.Canon = db
	if os.Getenv("LINA_M1_TELEGRAM_BOUNDARY") == "before_commit" {
		storage = telegramCommitBoundary{db, t}
	}
	g := gateway.New(storage, func(ctx context.Context, d gateway.Delivery) error {
		if d.Agent != os.Getenv("LINA_M1_TELEGRAM_AGENT") || d.Person != "person" {
			t.Fatalf("wrong agent: %+v", d)
		}
		if err := turn.New(db).Claim(ctx, "turn", d.Conversation, d.InboxID); err != nil {
			return err
		}
		acknowledgeCrash(t)
		return nil
	})
	a, err := telegram.New(telegram.Config{Bot: acceptanceBot, Token: secret.New("123456:synthetic_acceptance_token"), DB: db, Epoch: 2, Transport: pairingTransport(func(*http.Request) (*http.Response, error) { return telegramWire(acceptanceTelegramMessage), nil })})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Run(t.Context(), g); err != nil {
		t.Fatal(err)
	}
}

// Serial: Telegram polling is guarded process-wide by the shared bot ID.
func TestM1TelegramOwnerButton(t *testing.T) {
	h := newHarness(t, "")
	agent := seedAcceptanceTelegram(t, h.root)
	db, _ := acceptanceStore(t, h.root, 2)
	auth, err := authority.Open(h.ctx, db, authority.Config{Epoch: 2, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("no dispatch")
	}})
	if err != nil {
		t.Fatal(err)
	}
	approval := authority.Approval{ID: "request_123", EffectKey: "effect", Person: "person", Agent: agent, Epoch: 2, State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny}, Arguments: json.RawMessage(`{}`)}
	raw, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(h.ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(h.ctx, "INSERT INTO authority_approvals VALUES(?,?,?,?)", approval.ID, approval.EffectKey, "fingerprint", raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(h.ctx)
	defer cancel()
	polls, edits := 0, 0
	a, err := telegram.New(telegram.Config{Bot: acceptanceBot, Token: secret.New("123456:synthetic_acceptance_token"), DB: db, Epoch: 2, Approvals: auth, Transport: pairingTransport(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "getUpdates"):
			polls++
			if polls == 2 {
				stored, err := auth.Approval(ctx, approval.ID)
				if err != nil || stored.State != authority.Pending || edits != 0 {
					t.Error("outsider changed approval or removed buttons")
				}
			}
			if polls > 2 {
				cancel()
				return nil, ctx.Err()
			}
			sender := 200
			if polls == 2 {
				sender = 100
			}
			update, err := json.Marshal(map[string]any{"ok": true, "result": []any{map[string]any{"update_id": polls, "callback_query": map[string]any{"id": "callback", "from": map[string]int{"id": sender}, "data": "request_123:o", "message": map[string]any{"message_id": 19, "date": 1, "from": map[string]any{"id": 123456, "is_bot": true}, "chat": map[string]any{"id": 100, "type": "private"}}}}}})
			if err != nil {
				return nil, err
			}
			return telegramWire(string(update)), nil
		case strings.HasSuffix(r.URL.Path, "sendMessage"):
			return telegramWire(`{"ok":true,"result":{"message_id":19}}`), nil
		case strings.HasSuffix(r.URL.Path, "editMessageText"):
			edits++
			return telegramWire(`{"ok":true,"result":{"message_id":19}}`), nil
		default:
			return telegramWire(`{"ok":true,"result":true}`), nil
		}
	})})
	if err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	if result, err := a.PresentApproval(ctx, l, acceptanceChat, approval.ID); err != nil || result.Outcome != gateway.Delivered {
		t.Fatalf("presentation: %+v %v", result, err)
	}
	g := gateway.New(db, func(context.Context, gateway.Delivery) error {
		t.Error("button created a conversation turn")
		return nil
	})
	if err := a.Run(ctx, g); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stored, err := auth.Approval(h.ctx, approval.ID)
	if err != nil || stored.State != authority.Approved || stored.AnsweredBy != "person" || stored.ChatID != "100" || edits != 1 {
		t.Fatalf("owner decision: %+v edits=%d: %v", stored, edits, err)
	}
}
