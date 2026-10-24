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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/channels"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"github.com/thisisjun786/lina/kit/secret"
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

// testHub is a hub without a socket, for checkpoint tests.
func testHub(bot channels.Bot, db channels.Canon) *hub {
	return &hub{bot: bot, db: db, stop: make(chan struct{}), done: make(chan struct{})}
}

// addMember starts a subscriber's delivery worker.
func (h *hub) addMember(intents Intents, handle Handler) *member {
	m := &member{intents: intents, handle: handle, done: make(chan error, 1)}
	m.cond = sync.NewCond(&m.mu)
	h.members = append(h.members, m)
	go h.work(m)
	return m
}

// waitCheckpoint waits for the shared checkpoint to reach seq.
func waitCheckpoint(t *testing.T, h *hub, seq int64) cursor {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := h.resume(t.Context()); got.Seq == seq {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("checkpoint = %+v, want seq %d", h.resume(t.Context()), seq)
	return cursor{}
}

// A subscriber that must wait for its own store cannot hold up the socket or
// the other subscriber sharing the token.
func TestHeldSubscriberDoesNotBlockTheSession(t *testing.T) {
	frames := make(chan any, 4)
	ready := make(chan struct{}, 1)
	beat := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var identify struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &identify) != nil {
			return
		}
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		ready <- struct{}{}
		written := make(chan struct{}, 4)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case frame := <-frames:
					if wsjson.Write(ctx, conn, frame) != nil {
						return
					}
					written <- struct{}{}
				}
			}
		}()
		<-written
		<-written
		// The client must answer this while a subscriber is still holding a
		// dispatch, which proves the read loop is not blocked behind it.
		if wsjson.Write(ctx, conn, map[string]any{"op": 1, "d": nil}) != nil {
			return
		}
		var reply struct {
			Op int `json:"op"`
		}
		if wsjson.Read(ctx, conn, &reply) == nil && reply.Op == 1 {
			beat <- struct{}{}
		}
		<-ctx.Done()
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

	release := make(chan struct{})
	held := make(chan struct{}, 1)
	holding := make(chan error, 1)
	go func() {
		holding <- Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: baseIntents, Handle: func(_ context.Context, kind string, data json.RawMessage, _ string) error {
			var m struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(data, &m)
			if kind == "MESSAGE_CREATE" && m.ID == "1" {
				held <- struct{}{}
				<-release
			}
			return nil
		}})
	}()
	select {
	case <-ready:
	case err := <-holding:
		t.Fatalf("holding subscriber stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("holding subscriber never connected")
	}

	got := make(chan string, 4)
	g := gateway.New(f.db, func(_ context.Context, d gateway.Delivery) error {
		got <- d.NativeID
		return nil
	})
	go func() { _ = f.a.Run(ctx, g) }()
	waitMembers(t, f.db, f.a.bot, 2)

	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "first"}}
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the holding subscriber never received the first dispatch")
	}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 3, "d": map[string]any{"id": "2", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "second"}}
	for i := 0; i < 2; i++ {
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatalf("the other subscriber received %d of 2 dispatches while one was held", i)
		}
	}
	select {
	case <-beat:
	case <-time.After(5 * time.Second):
		t.Fatal("the socket did not answer a heartbeat while a dispatch was held")
	}
	if got := (&hub{bot: f.a.bot, db: f.db}).resume(t.Context()); got.Seq != 1 {
		t.Fatalf("checkpoint advanced past a held dispatch: %+v", got)
	}
	close(release)
	waitCheckpoint(t, &hub{bot: f.a.bot, db: f.db}, 3)
}

// A channel receipt must not acknowledge a dispatch that the source refused.
func TestSharedCheckpointWaitsForEverySubscriber(t *testing.T) {
	f := setup(t, nil)
	h := testHub(f.a.bot, f.db)
	in := gateway.New(f.db, func(context.Context, gateway.Delivery) error { return nil })
	h.addMember(0, func(ctx context.Context, kind string, data json.RawMessage, at string) error {
		return f.a.dispatch(ctx, in, kind, data, at)
	})
	h.addMember(0, func(_ context.Context, kind string, _ json.RawMessage, _ string) error {
		if kind == "MESSAGE_DELETE" {
			return errResponse
		}
		return nil
	})
	position := cursor{Session: "session-1", Seq: 2, Intents: baseIntents}
	h.broadcast(t.Context(), "MESSAGE_CREATE", json.RawMessage(`{"id":"1","channel_id":"100","author":{"id":"200"},"content":"hello"}`), position)
	waitCheckpoint(t, h, 2)
	position.Seq = 3
	h.broadcast(t.Context(), "MESSAGE_DELETE", json.RawMessage(`{"id":"1","channel_id":"100"}`), position)
	// The refusal stops the session, and the checkpoint stays at the last
	// dispatch every subscriber accepted.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !h.stopped() {
		time.Sleep(5 * time.Millisecond)
	}
	if !h.stopped() {
		t.Fatal("a refused dispatch did not stop the session")
	}
	if got := (&hub{bot: f.a.bot, db: f.db}).resume(t.Context()); got.Seq != 2 {
		t.Fatalf("shared checkpoint = %+v, want seq 2", got)
	}
}

// No channel connection exists for a source-only bot. Recreating the hub must
// retain its last completely delivered dispatch, including after invalidation.
func TestSourceOnlyCheckpointSurvivesHubRecreation(t *testing.T) {
	f := setup(t, nil)
	bot := channels.Bot{Platform: "discord", Account: "987654"}
	h := testHub(bot, f.db)
	position := cursor{Session: "source-session", Seq: 7, Intents: baseIntents | IntentMessageContent}
	h.broadcast(t.Context(), "MESSAGE_DELETE", nil, position)
	waitCheckpoint(t, h, 7)
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

// Discord refuses the privileged message-content intent with close 4014. Only
// the subscriber that asked for it loses text; the session reconnects with the
// other subscribers' intents, so a channel worker already running on the same
// token keeps its session instead of stopping with the source.
func TestDisallowedContentIntentKeepsTheChannelWorker(t *testing.T) {
	var accepts atomic.Int32
	channelReady := make(chan struct{}, 1)
	reconnected := make(chan chan any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		n := accepts.Add(1)
		frames := make(chan any, 4)
		if n == 3 {
			reconnected <- frames
		}
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
		switch n {
		case 2:
			// The source joined and asked for the content intent, which the bot
			// is not granted.
			if identify.D.Intents&int(IntentMessageContent) == 0 {
				t.Errorf("the source session did not ask for content: %d", identify.D.Intents)
			}
			conn.Close(4014, "disallowed intents")
			return
		case 3:
			if identify.D.Intents&int(IntentMessageContent) != 0 {
				t.Errorf("the reconnect kept the refused intent: %d", identify.D.Intents)
			}
		}
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session-1", "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		if n == 1 {
			channelReady <- struct{}{}
		}
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

	channelGot := make(chan struct{}, 1)
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { channelGot <- struct{}{}; return nil })
	channelDone := make(chan error, 1)
	go func() { channelDone <- f.a.Run(ctx, g) }()
	select {
	case <-channelReady:
	case err := <-channelDone:
		t.Fatalf("the channel worker stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the channel worker never connected")
	}

	// The source joins the running session and asks for the refused intent.
	sourceDone := make(chan error, 1)
	go func() {
		sourceDone <- Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: IntentMessageContent,
			Handle: func(context.Context, string, json.RawMessage, string) error { return nil }})
	}()
	select {
	case err := <-sourceDone:
		if !errors.Is(err, ErrIntentsDenied) {
			t.Fatalf("source stopped with %v, want the refused intent (sessions=%d)", err, accepts.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the source never learned that its intent was refused")
	}

	// The channel worker must still hold a live session on the same token.
	var frames chan any
	select {
	case frames = <-reconnected:
	case err := <-channelDone:
		t.Fatalf("the channel worker stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the channel worker never reconnected")
	}
	frames <- map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "hello"}}
	select {
	case <-channelGot:
	case err := <-channelDone:
		t.Fatalf("the channel worker stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the channel never received an event after the refused intent")
	}
	if accepts.Load() != 3 {
		t.Fatalf("gateway sessions = %d, want three (channel, refused source, channel again)", accepts.Load())
	}
	cancel()
	select {
	case err := <-channelDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("the channel worker stopped with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the channel worker did not stop")
	}
}

// A subscriber that never stores a dispatch must not grow Core's memory without
// limit: past the backlog the session stops and the next one replays from the
// last accepted checkpoint.
func TestSubscriberBacklogIsBounded(t *testing.T) {
	f := setup(t, nil)
	h := testHub(f.a.bot, f.db)
	blocked := make(chan struct{})
	defer close(blocked)
	h.addMember(0, func(context.Context, string, json.RawMessage, string) error {
		<-blocked
		return channels.Retry(errors.New("held"))
	})
	for seq := 1; seq <= memberBacklog+5 && !h.stopped(); seq++ {
		h.broadcast(t.Context(), "MESSAGE_CREATE", json.RawMessage(`{"id":"1","channel_id":"100"}`), cursor{Session: "session-1", Seq: int64(seq), Intents: baseIntents})
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !h.stopped() {
		time.Sleep(5 * time.Millisecond)
	}
	if !h.stopped() {
		t.Fatal("an unbounded backlog did not stop the session")
	}
	h.pendingMu.Lock()
	pending := len(h.pending)
	h.pendingMu.Unlock()
	// One dispatch may be in the worker's hand and one in flight when the
	// session stops; the backlog itself is bounded.
	if pending > memberBacklog+2 {
		t.Fatalf("pending dispatches = %d, want at most %d", pending, memberBacklog+2)
	}
}

// A stalled REST answer cannot hold a scheduled read forever.
func TestRestAttemptIsBounded(t *testing.T) {
	stalled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stalled:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(stalled)
	endpoint, _ := url.Parse(server.URL)
	client, err := NewClient(secret.New("synthetic.discord.token"), transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	if err != nil {
		t.Fatal(err)
	}
	client.AttemptTimeout = 100 * time.Millisecond
	client.Attempts = 1
	start := time.Now()
	if err := client.Get(t.Context(), "/users/@me", nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stalled read = %v, want unavailable", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("stalled read took %v", elapsed)
	}
}

// A checkpoint write that is older than the stored one never rewinds it.
func TestCheckpointNeverRewinds(t *testing.T) {
	f := setup(t, nil)
	h := testHub(f.a.bot, f.db)
	if err := h.advance(t.Context(), cursor{Session: "session-1", Seq: 5, Intents: baseIntents}); err != nil {
		t.Fatal(err)
	}
	if err := h.advance(t.Context(), cursor{Session: "session-1", Seq: 4, Intents: baseIntents}); err != nil {
		t.Fatal(err)
	}
	if got := (&hub{bot: f.a.bot, db: f.db}).resume(t.Context()); got.Seq != 5 {
		t.Fatalf("checkpoint = %+v, want seq 5", got)
	}
}

// A dispatch a subscriber could not store is not accepted when the session
// stops: the checkpoint stays before it, so the next session replays it.
func TestStoppingSessionDoesNotAcceptAnUnstoredDispatch(t *testing.T) {
	f := setup(t, nil)
	h := testHub(f.a.bot, f.db)
	attempted := make(chan struct{}, 1)
	h.addMember(0, func(context.Context, string, json.RawMessage, string) error {
		select {
		case attempted <- struct{}{}:
		default:
		}
		return channels.Retry(errors.New("write pending"))
	})
	h.broadcast(t.Context(), "MESSAGE_CREATE", json.RawMessage(`{"id":"1","channel_id":"100"}`), cursor{Session: "session-1", Seq: 2, Intents: baseIntents})
	select {
	case <-attempted:
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber never received the dispatch")
	}
	// The session stops while the subscriber is still retrying the dispatch.
	h.stopOnce.Do(func() { close(h.stop) })
	h.settle()
	if got := (&hub{bot: f.a.bot, db: f.db}).resume(t.Context()); got.Seq >= 2 {
		t.Fatalf("checkpoint = %+v, want it before the unstored dispatch", got)
	}
}
