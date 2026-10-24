// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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
