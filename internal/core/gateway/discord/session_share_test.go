// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
)

// TestOneGatewaySessionServesAChannelAndASource proves that a channel binding
// and a messenger source on the same bot token share one Gateway session: the
// second subscriber joins the running socket, and every event reaches both.
func TestOneGatewaySessionServesAChannelAndASource(t *testing.T) {
	var accepts atomic.Int32
	ready := make(chan struct{}, 2)
	frames := make(chan any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		accepts.Add(1)
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var identify struct {
			Op int `json:"op"`
			D  struct {
				Intents int `json:"intents"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &identify) != nil {
			return
		}
		if identify.Op != 2 || identify.D.Intents&int(IntentMessageContent) == 0 {
			t.Errorf("identify = %+v, want the source's content intent", identify)
			return
		}
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		ready <- struct{}{}
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if wsjson.Write(ctx, conn, frame) != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	f := setup(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sourceGot := make(chan string, 1)
	sourceDone := make(chan error, 1)
	go func() {
		sourceDone <- Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: IntentMessageContent, Handle: func(_ context.Context, kind string, data json.RawMessage, _ string) error {
			if kind != "MESSAGE_CREATE" {
				return nil
			}
			var m struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(data, &m)
			sourceGot <- kind + ":" + m.ID
			return nil
		}})
	}()
	select {
	case <-ready:
	case err := <-sourceDone:
		t.Fatalf("source session stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("source session never connected")
	}

	channelGot := make(chan struct{}, 1)
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { channelGot <- struct{}{}; return nil })
	channelDone := make(chan error, 1)
	go func() { channelDone <- f.a.Run(ctx, g) }()
	waitMembers(t, f.db, f.a.bot, 2)

	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "hello"}}
	select {
	case got := <-sourceGot:
		if got != "MESSAGE_CREATE:1" {
			t.Fatalf("source event = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the source never received the shared event")
	}
	select {
	case <-channelGot:
	case <-time.After(5 * time.Second):
		t.Fatal("the channel never received the shared event")
	}
	if accepts.Load() != 1 {
		t.Fatalf("gateway sessions = %d, want one", accepts.Load())
	}

	cancel()
	for name, done := range map[string]chan error{"source": sourceDone, "channel": channelDone} {
		select {
		case err := <-done:
			if err != nil && err != context.Canceled {
				t.Fatalf("%s stopped with %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not stop", name)
		}
	}
}

// A channel receipt must not acknowledge a dispatch that the source refused.
func TestSharedCheckpointWaitsForEverySubscriber(t *testing.T) {
	f := setup(t, nil)
	h := &hub{bot: f.a.bot, db: f.db}
	in := gateway.New(f.db, func(context.Context, gateway.Delivery) error { return nil })
	h.members = []*member{{handle: func(ctx context.Context, kind string, data json.RawMessage, at string) error {
		return f.a.dispatch(ctx, in, kind, data, at)
	}}, {handle: func(_ context.Context, kind string, _ json.RawMessage, _ string) error {
		if kind == "MESSAGE_DELETE" {
			return errResponse
		}
		return nil
	}}}
	// Use the cursor's actual wire shape, independent of field tag spelling.
	position := cursor{Session: "session-1", Seq: 2, Intents: baseIntents}
	raw, _ := json.Marshal(position)
	first := string(raw)
	if err := h.deliver(t.Context(), "MESSAGE_CREATE", json.RawMessage(`{"id":"1","channel_id":"100","author":{"id":"200"},"content":"hello"}`), first); err != nil {
		t.Fatal(err)
	}
	position.Seq = 3
	raw, _ = json.Marshal(position)
	if err := h.deliver(t.Context(), "MESSAGE_DELETE", json.RawMessage(`{"id":"1","channel_id":"100"}`), string(raw)); err == nil {
		t.Fatal("refused dispatch succeeded")
	}
	next := &hub{bot: f.a.bot, db: f.db}
	if got := next.resume(t.Context()); got.Seq != 2 {
		t.Fatalf("shared checkpoint = %+v, want seq 2", got)
	}
}

// No channel connection exists for a source-only bot. Recreating the hub must
// retain its last completely delivered dispatch, including after invalidation.
func TestSourceOnlyCheckpointSurvivesHubRecreation(t *testing.T) {
	f := setup(t, nil)
	bot := channels.Bot{Platform: "discord", Account: "987654"}
	h := &hub{bot: bot, db: f.db}
	position := cursor{Session: "source-session", Seq: 7, Intents: baseIntents | IntentMessageContent}
	raw, _ := json.Marshal(position)
	if err := h.deliver(t.Context(), "MESSAGE_DELETE", nil, string(raw)); err != nil {
		t.Fatal(err)
	}
	h.keep(position)
	next := &hub{bot: bot, db: f.db}
	if got := next.resume(t.Context()); got != position {
		t.Fatalf("recreated checkpoint = %+v, want %+v", got, position)
	}
	if err := next.reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := (&hub{bot: bot, db: f.db}).resume(t.Context()); got.Session != "" {
		t.Fatalf("reset retained session: %+v", got)
	}
}

func TestLastBaseSubscriberCancelsSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	h := &hub{stop: make(chan struct{}), done: make(chan struct{}), socket: cancel}
	m := &member{intents: baseIntents}
	h.members = []*member{m}
	go func() { <-ctx.Done(); close(h.done) }()
	detached := make(chan struct{})
	go func() { h.detach(m); close(detached) }()
	select {
	case <-detached:
	case <-time.After(time.Second):
		cancel()
		<-detached
		t.Fatal("last base-intent subscriber did not cancel the socket")
	}
}

func waitMembers(t *testing.T, db channels.Canon, bot channels.Bot, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if members(db, bot) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("session members = %d, want %d", members(db, bot), want)
}

func members(db channels.Canon, bot channels.Bot) int {
	hubs.Lock()
	h := hubs.byKey[hubKey{db: db, bot: bot.Key()}]
	hubs.Unlock()
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.members)
}

// A subscriber that cannot store a dispatch yet asks the session to replay it.
// The replay must neither acknowledge the dispatch early nor fail the session:
// a later dispatch still arrives on the same socket.
func TestRetryableDispatchReplaysWithoutFailingTheSession(t *testing.T) {
	ready := make(chan struct{}, 1)
	frames := make(chan any, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var first struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &first) != nil {
			return
		}
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		ready <- struct{}{}
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-frames:
				if wsjson.Write(ctx, conn, frame) != nil {
					return
				}
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	f := setup(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts atomic.Int32
	refused := make(chan struct{}, 1)
	got := make(chan string, 4)
	done := make(chan error, 1)
	go func() {
		done <- Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: 0,
			Handle: func(_ context.Context, kind string, data json.RawMessage, _ string) error {
				if kind != "MESSAGE_CREATE" {
					return nil
				}
				var m struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(data, &m)
				if m.ID == "1" && attempts.Add(1) < 3 {
					refused <- struct{}{}
					return channels.Retry(errors.New("write pending"))
				}
				got <- kind + ":" + m.ID
				return nil
			}})
	}()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("session stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("session never connected")
	}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100"}}
	select {
	case <-refused:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never saw the dispatch")
	}
	// The refusal must not move the checkpoint past the dispatch.
	var saved string
	if err := f.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT cursor FROM discord_sessions WHERE bot=?", f.a.bot.Key()).Scan(&saved)
	}); err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	var at cursor
	if saved != "" && json.Unmarshal([]byte(saved), &at) != nil {
		t.Fatalf("checkpoint = %q", saved)
	}
	if at.Seq != 1 {
		t.Fatalf("a refused dispatch advanced the checkpoint to %q", saved)
	}
	select {
	case g := <-got:
		if g != "MESSAGE_CREATE:1" {
			t.Fatalf("replayed dispatch = %q", g)
		}
	case err := <-done:
		t.Fatalf("the shared session failed on a retryable refusal: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the dispatch was not replayed")
	}
	if attempts.Load() != 3 {
		t.Fatalf("handler attempts = %d, want 3", attempts.Load())
	}
	// The session is still alive for the next dispatch.
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 3, "d": map[string]any{"id": "2", "channel_id": "100"}}
	select {
	case g := <-got:
		if g != "MESSAGE_CREATE:2" {
			t.Fatalf("next dispatch = %q", g)
		}
	case err := <-done:
		t.Fatalf("the shared session stopped after a replay: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the next dispatch never arrived")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("session stopped with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop")
	}
}
