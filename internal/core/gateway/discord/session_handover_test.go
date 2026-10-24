// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// TestIntentChangeEndsTheOldSessionBeforeIdentifying proves that a socket the
// hub replaces for a changed intent set is closed first: the platform must not
// run two sessions for one bot token, not even while the intents change.
func TestIntentChangeEndsTheOldSessionBeforeIdentifying(t *testing.T) {
	var accepts, open, maxOpen atomic.Int32
	ready := make(chan int, 2)
	intents := make(chan int, 2)
	// hold keeps the first socket from reading, so the platform has not
	// answered the close frame while the test looks for a second session.
	hold := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		n := int(accepts.Add(1))
		count := open.Add(1)
		for {
			max := maxOpen.Load()
			if count <= max || maxOpen.CompareAndSwap(max, count) {
				break
			}
		}
		defer open.Add(-1)
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
			D  struct {
				Intents int `json:"intents"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		intents <- auth.D.Intents
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", n), "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		ready <- n
		if n == 1 {
			select {
			case <-hold:
			case <-ctx.Done():
				return
			}
		}
		var frame struct{ Op int }
		for {
			if wsjson.Read(ctx, conn, &frame) != nil {
				return
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	f := setup(t, transport)
	ctx, cancel := context.WithCancel(context.Background())
	var listeners sync.WaitGroup
	start := func(asked Intents) {
		listeners.Add(1)
		go func() {
			defer listeners.Done()
			_ = Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: asked,
				Handle: func(context.Context, string, json.RawMessage, string) error { return nil }})
		}()
	}
	start(0)
	if n := <-ready; n != 1 {
		t.Fatalf("first session = %d", n)
	}
	if got := <-intents; got != int(baseIntents) {
		t.Fatalf("first Identify intents = %d, want %d", got, baseIntents)
	}
	// Capture the running socket before the second subscriber attaches, so the
	// assertion below waits for the hub's own handover request instead of
	// racing the attach.
	running := runningSocket(t, f)
	start(IntentMessageContent)
	select {
	case <-running.replace:
	case <-time.After(5 * time.Second):
		t.Fatal("the intent change never asked the running socket to hand over")
	}
	// The second subscriber changes the union, so the hub replaces the socket.
	// Until the platform answers the close frame, it must see one session.
	time.Sleep(250 * time.Millisecond)
	if got := accepts.Load(); got != 1 {
		t.Fatalf("sessions before the close was answered = %d, want the old one only", got)
	}
	close(hold)
	select {
	case n := <-ready:
		if n != 2 {
			t.Fatalf("replacement session = %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement session never connected")
	}
	if got := <-intents; got != int(baseIntents)|int(IntentMessageContent) {
		t.Fatalf("replacement Identify intents = %d, want %d", got, baseIntents|IntentMessageContent)
	}
	if got := maxOpen.Load(); got != 1 {
		t.Fatalf("concurrent Gateway sessions = %d, want one per bot token", got)
	}
	cancel()
	listeners.Wait()
}

// runningSocket returns the socket the hub for this fixture has published and
// not yet replaced, so a test can wait for the hub's own handover request.
func runningSocket(t *testing.T, f *fixture) *live {
	t.Helper()
	hubs.Lock()
	h := hubs.byKey[hubKey{db: f.db, bot: f.a.bot.Key()}]
	hubs.Unlock()
	if h == nil {
		t.Fatal("no hub runs for this bot token")
	}
	h.mu.Lock()
	l := h.socket
	h.mu.Unlock()
	if l == nil {
		t.Fatal("the hub has no running socket")
	}
	return l
}

// TestIntentChangeDuringTheDialDoesNotHoldTheLastSubscriber proves that a
// replacement requested while the connection attempt is still pending cancels
// that attempt, so the subscribers that leave afterwards return instead of
// waiting for the dial timeout.
func TestIntentChangeDuringTheDialDoesNotHoldTheLastSubscriber(t *testing.T) {
	entered := make(chan struct{}, 2)
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	f := setup(t, transport)
	start := func(ctx context.Context, asked Intents) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: asked,
				Handle: func(context.Context, string, json.RawMessage, string) error { return nil }})
		}()
		return done
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := start(firstCtx, 0)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first listener never dialed")
	}
	// The privileged intent changes the union while that dial is pending, so
	// the hub cancels the attempt and dials again with the whole union. A hub
	// that only replaces a socket it already established would leave the
	// pending attempt running.
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	defer cancelSecond()
	secondDone := start(secondCtx, IntentMessageContent)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the replacement dial never started, so the pending attempt was not cancelled")
	}
	// With both subscribers gone, neither Listen may wait for the dial timeout.
	cancelFirst()
	cancelSecond()
	for i, done := range []chan struct{}{firstDone, secondDone} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("listener %d still waits for a pending dial", i+1)
		}
	}
}

// TestUnchangedDetachKeepsTheSocketForTheNextIntentChange proves that a
// subscriber leaving without changing the intent union leaves the live socket
// reachable, so a later intent change still replaces it with a fresh Identify.
func TestUnchangedDetachKeepsTheSocketForTheNextIntentChange(t *testing.T) {
	var accepts atomic.Int32
	ready := make(chan int, 4)
	intents := make(chan int, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		n := int(accepts.Add(1))
		if wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
			D  struct {
				Intents int `json:"intents"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		intents <- auth.D.Intents
		if wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", n), "user": map[string]any{"id": "123456", "bot": true}}}) != nil {
			return
		}
		ready <- n
		var frame struct{ Op int }
		for {
			if wsjson.Read(ctx, conn, &frame) != nil {
				return
			}
		}
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	})
	f := setup(t, transport)
	start := func(ctx context.Context, asked Intents) chan struct{} {
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = Listen(ctx, Listener{Bot: f.a.bot, Token: f.a.token, DB: f.db, Transport: transport, Intents: asked,
				Handle: func(context.Context, string, json.RawMessage, string) error { return nil }})
		}()
		return done
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	start(firstCtx, 0)
	if n := <-ready; n != 1 {
		t.Fatalf("first session = %d", n)
	}
	if got := <-intents; got != int(baseIntents) {
		t.Fatalf("first Identify intents = %d, want %d", got, baseIntents)
	}
	// A second subscriber with the same intents leaves the union unchanged, and
	// so does its departure.
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := start(secondCtx, 0)
	select {
	case n := <-ready:
		t.Fatalf("a subscriber with the same intents opened session %d", n)
	case <-time.After(250 * time.Millisecond):
	}
	cancelSecond()
	<-secondDone
	// The privileged intent changes the union, so the hub must replace the
	// socket that is still running from before the unchanged departure.
	thirdCtx, cancelThird := context.WithCancel(context.Background())
	defer cancelThird()
	start(thirdCtx, IntentMessageContent)
	select {
	case n := <-ready:
		if n != 2 {
			t.Fatalf("replacement session = %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the intent change after an unchanged departure never reconnected")
	}
	if got := <-intents; got != int(baseIntents)|int(IntentMessageContent) {
		t.Fatalf("replacement Identify intents = %d, want %d", got, baseIntents|IntentMessageContent)
	}
}
