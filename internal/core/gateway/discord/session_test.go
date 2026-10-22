// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionResumesCommittedCursorAndDeduplicates(t *testing.T) {
	var connects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if err = wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}); err != nil {
			return
		}
		var auth struct {
			Op int `json:"op"`
			D  struct {
				Session string `json:"session_id"`
				Seq     int64  `json:"seq"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		n := connects.Add(1)
		if n == 1 {
			if auth.Op != 2 {
				t.Error("first session did not identify")
			}
			_ = wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": "session", "user": map[string]any{"id": "123456", "bot": true}}})
		} else {
			if auth.Op != 6 || auth.D.Session != "session" || auth.D.Seq != 2 {
				t.Errorf("incorrect resume: %+v", auth)
			}
		}
		_ = wsjson.Write(ctx, conn, map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "hello"}})
		op := 7
		if n == 2 {
			op = 9
		}
		_ = wsjson.Write(ctx, conn, map[string]any{"op": op, "d": false})
		_, _, _ = conn.Read(ctx)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) {
		wire := r.Clone(r.Context())
		wire.URL.Scheme = endpoint.Scheme
		wire.URL.Host = endpoint.Host
		return http.DefaultTransport.RoundTrip(wire)
	}))
	calls := 0
	g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { calls++; return nil })
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		err := f.a.Run(ctx, g)
		cancel()
		if _, retry := RecoveryDelay(err); !retry {
			t.Fatalf("session not recoverable: %v", err)
		}
		saved, err := f.store.Cursor(t.Context(), f.a.bot)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			var c cursor
			if json.Unmarshal([]byte(saved), &c) != nil || c.Seq != 2 || c.Session != "session" {
				t.Fatalf("uncommitted cursor: %s", saved)
			}
		} else if saved != "" {
			t.Fatal("invalid session not cleared")
		}
	}
	if calls != 1 {
		t.Fatalf("resumed duplicate dispatches=%d", calls)
	}
}
