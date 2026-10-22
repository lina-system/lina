// SPDX-License-Identifier: Apache-2.0
package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/gateway"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionCloseRecovery(t *testing.T) {
	for _, code := range []websocket.StatusCode{4007, 4009, 4000, 4008, websocket.StatusNormalClosure} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			reset := code == 4007 || code == 4009
			var connects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				write := func(v any) bool {
					if err := wsjson.Write(ctx, conn, v); err != nil {
						t.Error(err)
						return false
					}
					return true
				}
				if !write(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}) {
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
				n := connects.Add(1)
				if n == 1 || reset {
					if auth.Op != 2 || auth.D.Session != "" || auth.D.Seq != 0 {
						t.Errorf("expected fresh Identify, got %+v", auth)
					}
					if !write(map[string]any{"op": 0, "t": "READY", "s": 1, "d": map[string]any{"session_id": fmt.Sprintf("session-%d", n), "user": map[string]any{"id": "123456", "bot": true}}}) {
						return
					}
				} else if auth.Op != 6 || auth.D.Session != "session-1" || auth.D.Seq != 2 {
					t.Errorf("expected committed Resume, got %+v", auth)
				}
				if !write(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 2, "d": map[string]any{"id": "1", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "hello"}}) {
					return
				}
				if n == 1 {
					_ = conn.Close(code, "synthetic close")
					return
				}
				if !write(map[string]any{"op": 0, "t": "MESSAGE_CREATE", "s": 3, "d": map[string]any{"id": "2", "channel_id": "100", "author": map[string]any{"id": "200"}, "content": "fresh work"}}) {
					return
				}
				write(map[string]any{"op": 7, "d": nil})
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
				if i == 0 && reset {
					if saved != "" {
						t.Errorf("non-resumable cursor retained after %d: %s", code, saved)
					}
				} else {
					want := cursor{Session: "session-1", Seq: 2 + int64(i)}
					if i == 1 && reset {
						want.Session = "session-2"
					}
					var got cursor
					if json.Unmarshal([]byte(saved), &got) != nil || got != want {
						t.Errorf("cursor=%s, want %+v", saved, want)
					}
				}
				var receipts int
				if err := f.db.Read(t.Context(), func(tx *canon.Tx) error {
					return tx.QueryRowContext(t.Context(), "SELECT count(*) FROM channel_receipts WHERE bot=?", f.a.bot.Key()).Scan(&receipts)
				}); err != nil {
					t.Fatal(err)
				}
				if receipts != i+1 || calls != i+1 {
					t.Fatalf("recovery lost receipts or replayed work: receipts=%d calls=%d", receipts, calls)
				}
			}
		})
	}
}

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
				Intents int    `json:"intents"`
			} `json:"d"`
		}
		if wsjson.Read(ctx, conn, &auth) != nil {
			return
		}
		n := connects.Add(1)
		if n == 1 {
			if auth.D.Intents&32768 != 0 {
				t.Error("private binding requested privileged message content")
			}
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
