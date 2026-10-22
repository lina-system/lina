// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/gateway"
)

func TestSessionCloseRecovery(t *testing.T) {
	for _, code := range []websocket.StatusCode{4007, 4009, 4000, websocket.StatusGoingAway} {
		for _, beforeHello := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/before_hello=%t", code, beforeHello), func(t *testing.T) {
				fresh := code == 4007 || code == 4009
				var connects atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					conn, err := websocket.Accept(w, r, nil)
					if err != nil {
						return
					}
					defer conn.CloseNow()
					ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
					defer cancel()
					n := connects.Add(1)
					if n == 2 && beforeHello {
						_ = conn.Close(code, "synthetic close")
						return
					}
					if err := wsjson.Write(ctx, conn, map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}); err != nil {
						t.Error(err)
						return
					}
					var auth struct {
						Op int `json:"op"`
						D  struct {
							Session string `json:"session_id"`
							Seq     int64  `json:"seq"`
						} `json:"d"`
					}
					if err := wsjson.Read(ctx, conn, &auth); err != nil {
						t.Error(err)
						return
					}
					identify := n == 1 || (n == 3 && fresh)
					if identify {
						if auth.Op != 2 || auth.D.Session != "" || auth.D.Seq != 0 {
							t.Errorf("connection %d must Identify: %+v", n, auth)
						}
					} else if auth.Op != 6 || auth.D.Session != "session-1" || auth.D.Seq != 2 {
						t.Errorf("connection %d must Resume committed cursor: %+v", n, auth)
					}
					if n == 2 {
						_ = conn.Close(code, "synthetic close")
						return
					}
					write := func(f any) {
						if err := wsjson.Write(ctx, conn, f); err != nil {
							t.Error(err)
						}
					}
					if identify {
						write(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", n), "user": map[string]any{"id": "123456", "bot": true}}})
					}
					message := func(id string, seq int) {
						write(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": seq, "d": map[string]any{"id": id, "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "hello"}})
					}
					message("1", 2)
					if n == 3 {
						message("2", 3)
					}
					write(map[string]any{"op": 7})
					_, _, _ = conn.Read(ctx)
				}))
				defer server.Close()
				endpoint, _ := url.Parse(server.URL)
				f := setup(t, transportFunc(func(r *http.Request) (*http.Response, error) {
					wire := r.Clone(r.Context())
					wire.URL.Scheme, wire.URL.Host = endpoint.Scheme, endpoint.Host
					return http.DefaultTransport.RoundTrip(wire)
				}))
				calls := 0
				g := gateway.New(f.db, func(context.Context, gateway.Delivery) error { calls++; return nil })
				for n := 1; n <= 3; n++ {
					ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
					err := f.a.Run(ctx, g)
					cancel()
					if _, retry := RecoveryDelay(err); !retry {
						t.Fatalf("connection %d not recoverable: %v", n, err)
					}
					saved, err := f.store.Cursor(t.Context(), f.a.bot)
					if err != nil {
						t.Fatal(err)
					}
					if n == 2 && fresh {
						if saved != "" {
							t.Fatalf("non-resumable cursor retained: %s", saved)
						}
						continue
					}
					want := cursor{Session: "session-1", Seq: 2}
					if n == 3 {
						want.Seq = 3
						if fresh {
							want.Session = "session-3"
						}
					}
					var got cursor
					if json.Unmarshal([]byte(saved), &got) != nil || got != want {
						t.Fatalf("connection %d cursor=%s, want %+v", n, saved, want)
					}
				}
				if calls != 2 {
					t.Fatalf("expected two unique deliveries across sessions, got %d", calls)
				}
			})
		}
	}
}
