// SPDX-License-Identifier: Apache-2.0

package boot

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/kit/rpc"
	"github.com/thisisjun786/lina/kit/secret"
	"io"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/exchange"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/protocol"
)

type channelTransport func(*http.Request) (*http.Response, error)

func (f channelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func controlRuntime(t *testing.T) *conversationRuntime {
	t.Helper()
	// Test names can exceed the Unix socket path limit once jobs.sock is added.
	root, err := os.MkdirTemp("", "lina-boot-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ctl.Close() })
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	r, err := startConversation(t.Context(), root, "test-main", 1, ctl, nil, lifecycle.NewRegistry(nil), "owner", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r
}
func str(s string) *string { return &s }
func TestInstalledChannelPairBindDisconnect(t *testing.T) {
	r := controlRuntime(t)
	updates := make(chan string, 1)
	polls := make(chan struct{}, 8)
	var rejectToken atomic.Bool
	r.channels.transport = channelTransport(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/getMe") && rejectToken.Load() {
			return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":401,"description":"unauthorized"}`))}, nil
		}
		if strings.HasSuffix(req.URL.Path, "/getMe") {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`))}, nil
		}
		if !strings.HasSuffix(req.URL.Path, "/getUpdates") {
			t.Errorf("unexpected platform method")
			return nil, context.Canceled
		}
		select {
		case polls <- struct{}{}:
		default:
		}
		select {
		case body := <-updates:
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	})
	session := clientsrv.Session{PersonID: r.person, CanDecide: true}
	call := func(p protocol.ChannelRequest) protocol.ChannelResult {
		t.Helper()
		op, ok := r.identity.Operations[protocol.KindChannelRequest]
		if !ok {
			t.Fatal("channel operation absent from boot")
		}
		result, err := op.Handle(t.Context(), session, protocol.Envelope{Payload: p})
		if err != nil {
			t.Fatal(err)
		}
		return result.Payload.(protocol.ChannelResult)
	}
	token := "123456:synthetic_token_for_test"
	connected := call(protocol.ChannelRequest{Operation: str("connect"), Token: &token})
	if len(*connected.Connections) != 1 {
		t.Fatal("connection not projected")
	}
	paired := call(protocol.ChannelRequest{Operation: str("pair"), Account: str("123456")})
	if paired.PairingCode == nil || paired.ExpiresAt == nil {
		t.Fatal("pairing code absent")
	}
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("poller did not start")
	}
	updates <- `{"ok":true,"result":[{"update_id":1,"message":{"message_id":1,"from":{"id":42,"is_bot":false,"first_name":"Owner"},"chat":{"id":42,"type":"private"},"date":1,"text":"` + *paired.PairingCode + `"}}]}`
	// A second poll begins only after pairing and its cursor are durable.
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("pairing was not committed")
	}
	bound := call(protocol.ChannelRequest{Operation: str("bind"), Account: str("123456"), AgentID: &r.agent, Scope: &protocol.ChannelScope{Reads: "dm_only", Writes: "reply_only", Tools: true, Memory: true}})
	row := (*bound.Connections)[0]
	if row.Status != "connected" || row.PrivateChat != "42" || len(row.Bindings) != 1 || row.Bindings[0].AgentID != r.agent {
		t.Fatal("paired binding not projected")
	}

	call(protocol.ChannelRequest{Operation: str("bind"), Account: str("123456"), Chat: str("-42"), AgentID: &r.agent, Scope: &protocol.ChannelScope{Reads: "addressed", Writes: "reply_only"}})
	updates <- `{"ok":true,"result":[{"update_id":2,"message":{"message_id":2,"from":{"id":42,"is_bot":false,"first_name":"Owner"},"chat":{"id":-42,"type":"group"},"date":1,"text":"@fixture_bot hello","entities":[{"type":"mention","offset":0,"length":12}]}}]}`
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("addressed input did not commit")
	}
	var admitted int
	if err := r.db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM inbox WHERE native_id='2'").Scan(&admitted)
	}); err != nil || admitted != 1 {
		t.Fatalf("addressed mention missing from inbox: %d %v", admitted, err)
	}
	raw, _ := json.Marshal(bound)
	if strings.Contains(string(raw), token) {
		t.Fatal("token leaked")
	}

	rejectToken.Store(true)
	if _, err := r.channels.Channel(t.Context(), session, protocol.ChannelRequest{Operation: str("connect"), Token: str("123456:wrong_replacement")}); err == nil {
		t.Fatal("unverified replacement accepted")
	}
	rejectToken.Store(false)
	stored, err := r.vault.Get(r.channels.key(channels.Bot{Platform: "telegram", Account: "123456"}))
	if err != nil {
		t.Fatal(err)
	}
	header := make(http.Header)
	if err := secret.InjectHeader(header, "Authorization", "", stored); err != nil || header.Get("Authorization") != token {
		t.Fatal("failed replacement lost the existing credential")
	}
	outsider := session
	outsider.PersonID = "other"
	if _, err := r.channels.Channel(t.Context(), outsider, protocol.ChannelRequest{Operation: str("list")}); err == nil {
		t.Fatal("foreign owner read controls")
	}
	if err := channels.New(r.db).Pair(t.Context(), channels.Bot{Platform: "telegram", Account: "123456"}, *paired.PairingCode, "42", "42", time.Now()); err == nil {
		t.Fatal("pairing code reused")
	}
	// Reopen the poller composition against durable connection and secret state.
	r.channels.Close()
	r.channels, err = startChannels(r, 1, r.channels.transport)
	if err != nil {
		t.Fatal(err)
	}
	r.identity.Operations[protocol.KindChannelRequest] = clientsrv.ChannelOperations(r.channels)[protocol.KindChannelRequest]
	select {
	case <-polls:
	case <-time.After(5 * time.Second):
		t.Fatal("persisted channel did not resume")
	}
	disconnected := call(protocol.ChannelRequest{Operation: str("disconnect"), Account: str("123456")})
	row = (*disconnected.Connections)[0]
	if row.Status != "disconnected" || len(row.Bindings) != 0 || row.PrivateChat != "" {
		t.Fatal("disconnect retained binding or pairing")
	}
	if _, err := r.vault.Get(r.channels.key(channels.Bot{Platform: "telegram", Account: "123456"})); err == nil {
		t.Fatal("disconnect retained token")
	}
}
func TestInstalledTalkListsAndExpandsDurableExchange(t *testing.T) {
	r := controlRuntime(t)
	peer, err := r.agents.Create(t.Context(), agents.Create{Name: "Peer", PersonID: r.person, Layers: agents.Layers{Core: "Compare supplied material"}})
	if err != nil {
		t.Fatal(err)
	}
	store := exchange.NewWithMaterials(r.db, r.materials)
	now := time.Now()
	receipt, err := store.Start(t.Context(), exchange.Request{DeliveryID: "test-delivery", From: r.agent, To: peer.ID, HandOff: exchange.HandOff{Subject: "Compare drafts", Wanted: "Find differences", OutOfScope: "No edits", Done: "All sections checked", Check: "sections", Deadline: now.Add(time.Hour)}}, now)
	if err != nil {
		t.Fatal(err)
	}
	session := clientsrv.Session{PersonID: r.person}
	result, err := r.identity.Operations[protocol.KindAgentTalkGet].Handle(t.Context(), session, protocol.Envelope{Payload: protocol.AgentTalkGet{}})
	if err != nil {
		t.Fatal(err)
	}
	rows := result.Payload.(protocol.AgentTalk).Entries
	if rows == nil || len(*rows) != 1 || (*rows)[0].ExchangeID != receipt.ID {
		t.Fatal("exchange summary absent")
	}
	expanded, err := r.identity.Operations[protocol.KindAgentTalkExpand].Handle(t.Context(), session, protocol.Envelope{Payload: protocol.AgentTalkExpand{ExchangeID: &receipt.ID}})
	if err != nil {
		t.Fatal(err)
	}
	events := expanded.Payload.(protocol.AgentTalk).Events
	if events == nil || len(*events) != 1 || (*events)[0].AgentID != r.agent || !strings.Contains((*events)[0].Text, "Compare drafts") {
		t.Fatal("durable exchange not expanded")
	}
	session.PersonID = "other"
	if _, err := r.ExpandTalk(t.Context(), session, protocol.AgentTalkExpand{ExchangeID: &receipt.ID}); err == nil {
		t.Fatal("foreign owner expanded exchange")
	}
}

func TestTalkPagesPreserveOversizedUnicodeBodiesWithinRPCBudget(t *testing.T) {
	text := strings.Repeat("한글<\n", 300000)
	message, err := guard.Admit(guard.EntryAgentExchange, "test", text)
	if err != nil {
		t.Fatal(err)
	}
	events := []exchange.Event{{Agent: "agent", Kind: "request", At: time.Now(), Message: message}}
	var cursor *string
	var assembled strings.Builder
	pages := 0
	for {
		p, err := talkPage("exchange", events, cursor)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(p)
		if err != nil || len(raw) >= rpc.MaxFrameBytes/2 || !utf8.ValidString((*p.Events)[0].Text) {
			t.Fatal("page exceeds wire budget or splits a rune")
		}
		assembled.WriteString((*p.Events)[0].Text)
		pages++
		if p.NextCursor == nil {
			break
		}
		if cursor != nil && *cursor == *p.NextCursor {
			t.Fatal("page made no progress")
		}
		cursor = p.NextCursor
	}
	if pages < 2 || assembled.String() != text {
		t.Fatal("paged expansion lost or duplicated content")
	}
	if _, err := talkPage("exchange", events, str("0:1")); err == nil {
		t.Fatal("accepted cursor inside UTF-8 rune")
	}
}
func TestChannelSupervisorRetriesReadsAndShowsTerminalFailure(t *testing.T) {
	r := controlRuntime(t)
	r.channels.transport = channelTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic read failure") })
	adapter, err := r.channels.adapter(channels.Bot{Platform: "telegram", Account: "123456"}, secret.New("123456:synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	retryable := adapter.Identify(t.Context())
	var healthy atomic.Bool
	calls, pauses := 0, 0
	superviseChannel(t.Context(), &healthy, func(context.Context) error {
		calls++
		healthy.Store(true)
		if calls == 1 {
			return retryable
		}
		return context.Canceled
	}, func(context.Context, time.Duration) error {
		pauses++
		if healthy.Load() {
			t.Fatal("failed worker still healthy")
		}
		return nil
	})
	if calls != 2 || pauses != 1 || healthy.Load() {
		t.Fatal("read failure was not retried or terminal state was hidden")
	}
	var identities atomic.Int32
	r.channels.transport = channelTransport(func(*http.Request) (*http.Response, error) {
		if identities.Add(1) == 1 {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":{"id":123456,"is_bot":true,"username":"fixture_bot"}}`))}, nil
		}
		return &http.Response{StatusCode: 401, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":false,"error_code":401,"description":"unauthorized"}`))}, nil
	})
	session := clientsrv.Session{PersonID: r.person, CanDecide: true}
	if _, err := r.channels.Channel(t.Context(), session, protocol.ChannelRequest{Operation: str("connect"), Token: str("123456:synthetic")}); err != nil {
		t.Fatal(err)
	}
	worker := r.channels.workers[channels.Bot{Platform: "telegram", Account: "123456"}.Key()]
	select {
	case <-worker.done:
	case <-time.After(5 * time.Second):
		t.Fatal("terminal worker did not stop")
	}
	result, err := r.channels.Channel(t.Context(), session, protocol.ChannelRequest{Operation: str("list")})
	if err != nil || (*result.Connections)[0].Status != "unavailable" {
		t.Fatal("dead worker reported connected")
	}
}
