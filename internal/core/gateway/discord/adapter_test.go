// SPDX-License-Identifier: Apache-2.0

package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/grant"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/secret"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(r *http.Request, status int, body string) *http.Response {
	return &http.Response{Request: r, StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type fixture struct {
	db        *canon.Store
	store     *channels.Store
	a         *Adapter
	address   channels.Address
	agent     string
	approvals *authority.Service
}

func setup(t *testing.T, tr http.RoundTripper) *fixture {
	t.Helper()
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 7, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	as, err := agents.Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { as.Close() })
	ag, err := as.Create(t.Context(), agents.Create{Name: "Synthetic", PersonID: "person", Layers: agents.Layers{Core: "core", DefaultVoice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	s := channels.New(db)
	bot := channels.Bot{Platform: "discord", Account: "123456"}
	address := channels.Address{Bot: bot, Chat: "100"}
	if err = s.Connect(t.Context(), bot, "person"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	code, err := s.PairingCode(t.Context(), bot, "person", now)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Pair(t.Context(), bot, code, "200", "100", now); err != nil {
		t.Fatal(err)
	}
	if err = s.Bind(t.Context(), channels.Binding{Address: address, Agent: ag.ID, Person: "person", Conversation: "conversation", Scope: channels.PrivateScope()}); err != nil {
		t.Fatal(err)
	}
	auth, err := authority.Open(t.Context(), db, authority.Config{Epoch: 7, Resolve: func(context.Context, string) (grant.Grant, grant.Environment, error) {
		return grant.Grant{}, grant.Environment{}, errors.New("unused")
	}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{Bot: bot, Token: secret.New("synthetic.discord.token"), DB: db, Epoch: 7, Approvals: auth, Transport: tr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return &fixture{db: db, store: s, a: a, address: address, agent: ag.ID, approvals: auth}
}
func TestShape(t *testing.T) {
	for _, text := range []string{strings.Repeat("a", 1500) + "\n\n" + strings.Repeat("b", 1000), strings.Repeat("😀", 1100), strings.Repeat("한글", 1600)} {
		parts, err := Shape(text, "", "")
		if err != nil || len(parts) < 2 || strings.Join(parts, "") != text {
			t.Fatalf("shape %d %v", len(parts), err)
		}
		for _, p := range parts {
			if units(p) > 2000 {
				t.Fatal("oversized part")
			}
		}
		if strings.Contains(text, "\n\n") && !strings.HasSuffix(parts[0], "\n\n") {
			t.Fatal("paragraph was split")
		}
	}
	large := strings.Repeat("x", 6001)
	if parts, err := Shape(large, "", ""); err != nil || len(parts) != 1 || parts[0] != large {
		t.Fatal("full file payload not retained")
	}
}
func TestDuplicateInteractionAndOwnerButtons(t *testing.T) {
	calls := 0
	f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return response(r, 200, `{"id":"300"}`), nil }))
	approval := authority.Approval{ID: "approval_1", EffectKey: "effect_1", Epoch: 7, Person: "person", Agent: f.agent, State: authority.Pending, Choices: []authority.Choice{authority.Once, authority.Deny}, What: "Send", Where: "destination", Arguments: json.RawMessage(`{}`)}
	data, _ := json.Marshal(approval)
	if err := f.db.Write(t.Context(), func(tx *canon.Tx) error {
		_, err := tx.ExecContext(t.Context(), "INSERT INTO authority_approvals(approval_id,effect_key,fingerprint,record) VALUES(?,?,?,?)", approval.ID, approval.EffectKey, "fingerprint", data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	if result, err := f.a.PresentApproval(t.Context(), l, f.address, approval.ID); err != nil || result.Outcome != gateway.Delivered {
		t.Fatalf("present: %+v %v", result, err)
	}
	dispatches := 0
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { dispatches++; return nil })
	event := func(id, user, custom string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"id": id, "application_id": "123456", "type": 3, "token": "interaction-secret", "channel_id": "100", "user": map[string]any{"id": user}, "message": map[string]any{"id": "300", "author": map[string]any{"id": "123456", "bot": true}}, "data": map[string]any{"custom_id": custom}})
		return b
	}
	if err := f.a.dispatch(t.Context(), g, "INTERACTION_CREATE", event("1", "999", "approval_1:o"), "cursor1"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.approvals.Approval(t.Context(), approval.ID)
	if got.State != authority.Pending {
		t.Fatal("outsider answered")
	}
	if calls != 1 {
		t.Fatal("outsider changed platform message")
	}

	for i, mutate := range []func(*interaction){
		func(q *interaction) { q.Message.Author.ID = "999" },
		func(q *interaction) { q.Message.ID = "999" },
		func(q *interaction) { q.Data.Custom = "another_request:o" },
		func(q *interaction) { q.Data.Custom = "approval_1:a" },
	} {
		var q interaction
		_ = json.Unmarshal(event(strconv.Itoa(i+10), "200", "approval_1:o"), &q)
		mutate(&q)
		raw, _ := json.Marshal(q)
		if err := f.a.dispatch(t.Context(), g, "INTERACTION_CREATE", raw, "forged"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		got, _ := f.approvals.Approval(t.Context(), approval.ID)
		if got.State != authority.Pending || calls != 1 {
			t.Fatal("forged button changed decision or message")
		}
	}
	if err := f.a.dispatch(t.Context(), g, "INTERACTION_CREATE", event("2", "200", "approval_1:o"), "cursor2"); err != nil {
		t.Fatal(err)
	}
	got, _ = f.approvals.Approval(t.Context(), approval.ID)
	if got.State != authority.Approved || got.AnsweredBy != "person" {
		t.Fatalf("owner: %+v", got)
	}
	before := calls
	if err := f.a.dispatch(t.Context(), g, "INTERACTION_CREATE", event("2", "200", "approval_1:o"), "cursor3"); err != nil {
		t.Fatal(err)
	}
	if calls != before || dispatches != 0 {
		t.Fatal("replayed interaction had effects")
	}
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		var n int
		err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE CAST(body AS TEXT) LIKE '%interaction-secret%'").Scan(&n)
		if n != 0 {
			t.Error("interaction token persisted")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func TestBlockedQueueMarksBinding(t *testing.T) {
	calls := 0
	f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return response(r, 403, `{"code":50007,"message":"Cannot send messages to this user"}`), nil
	}))
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { return nil })
	if err := g.Admit(t.Context(), gateway.Inbound{Address: f.address, NativeID: "1", Sender: "200", Cursor: "1", Direct: true, Body: []byte(`{"text":"hi"}`)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := ledger.New(f.db, nil).Admit(t.Context(), guard.EntryAgent, ledger.Input{ID: "reply", ConversationID: "conversation", AgentID: f.agent, Surface: ledger.Surface{Kind: "channel", Platform: "discord", ChatID: "100"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "reply"}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Enqueue(t.Context(), channels.Message{ID: "reply", Address: f.address, Agent: f.agent, LedgerRef: "reply", ReplyTo: "1", Parts: []string{"reply"}}); err != nil {
		t.Fatal(err)
	}
	l := lease.New(func() {})
	defer l.Release()
	q := gateway.NewQueue(f.db, f.a)
	if _, err := q.SendNext(t.Context(), l, f.address, now); err != nil {
		t.Fatal(err)
	}
	binding, err := f.store.Binding(t.Context(), f.address)
	if err != nil || !binding.Unreachable {
		t.Fatalf("binding %+v %v", binding, err)
	}
	_, _ = q.SendNext(t.Context(), l, f.address, now)
	if calls != 1 {
		t.Fatal("blocked message retried")
	}
}

func TestSendClassifiesWithoutRetryOrLeakingErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		out    gateway.Outcome
	}{
		{"flood", 429, `{"retry_after":12.25}`, gateway.RetryLater},
		{"overflow", 429, `{"retry_after":1e100}`, gateway.Uncertain},
		{"unknown", 503, `{"message":"synthetic.discord.token"}`, gateway.Uncertain},
		{"unauthorized", 401, `{}`, gateway.Unauthorized},
		{"invalid", 400, `{"code":50035}`, gateway.TooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return response(r, tc.status, tc.body), nil }))
			l := lease.New(func() {})
			defer l.Release()
			got, err := f.a.Send(t.Context(), l, gateway.Outbound{Address: f.address, Text: "hello"})
			if err != nil || got.Outcome != tc.out || calls != 1 {
				t.Fatalf("got %+v %v calls=%d", got, err, calls)
			}
			if tc.name == "flood" && got.RetryAfter != 12250*time.Millisecond {
				t.Fatal("retry-after shortened")
			}
		})
	}
}

func TestInboundDedupAndBotTokenMask(t *testing.T) {
	f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) { t.Fatal("unexpected I/O"); return nil, nil }))
	calls := 0
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { calls++; return nil })
	for _, id := range []string{"1", "1"} {
		data, _ := json.Marshal(map[string]any{"id": id, "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "echo synthetic.discord.token"})
		if err := f.a.dispatch(t.Context(), g, "MESSAGE_CREATE", data, "cursor"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("duplicate started work")
	}
	if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
		var body []byte
		err := tx.QueryRowContext(t.Context(), "SELECT body FROM inbox").Scan(&body)
		var e gateway.Inbound
		_ = json.Unmarshal(body, &e)
		if strings.Contains(string(e.Body), "synthetic.discord.token") {
			t.Error("bot token in inbox")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLongReplySendsOneCompleteFile(t *testing.T) {
	text := strings.Repeat("한글 paragraph\n\n", 700)
	calls := 0
	f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		file, header, err := r.FormFile("files[0]")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || string(data) != text || header.Filename != "reply.txt" {
			t.Fatal("attachment lost content")
		}
		return response(r, 200, `{"id":"301"}`), nil
	}))
	parts, err := Shape(text, "", "")
	if err != nil || len(parts) != 1 {
		t.Fatal("file shape", err)
	}
	l := lease.New(func() {})
	defer l.Release()
	result, err := f.a.Send(t.Context(), l, gateway.Outbound{Address: f.address, Text: parts[0]})
	if err != nil || result.Outcome != gateway.Delivered || calls != 1 {
		t.Fatalf("file send %+v %v calls=%d", result, err, calls)
	}
}
