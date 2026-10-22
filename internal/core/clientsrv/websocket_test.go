// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func websocketSession(t *testing.T, identity Identity) (*rpc.WebSocket, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		serveWebSocket(ctx, w, r, identity, peerIdentity{})
	}))
	wire, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		srv.Close()
		cancel()
		t.Fatal(err)
	}
	conn := rpc.NewWebSocket(wire)
	t.Cleanup(func() { conn.Close(); cancel(); <-done; srv.Close() })
	return conn, ctx
}

func websocketCall(t *testing.T, ctx context.Context, conn *rpc.WebSocket, envelope protocol.Envelope) protocol.Envelope {
	t.Helper()
	if err := conn.Write(ctx, map[string]any{"jsonrpc": "2.0", "id": "call", "method": envelope.Kind, "params": envelope}); err != nil {
		t.Fatal(err)
	}
	data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var answer response
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.JSONRPC != "2.0" || string(answer.ID) != `"call"` || answer.Result.ReplyTo == nil || *answer.Result.ReplyTo != envelope.MessageID {
		t.Fatal("uncorrelated response")
	}
	return answer.Result
}

func TestWebSocketSessionPreservesWelcomeAndAdmission(t *testing.T) {
	var recovering atomic.Bool
	var mutations atomic.Int32
	capability := protocol.Capability{Name: "conversation", Version: "1.0"}
	identity := Identity{MainID: "main-test", Epoch: "18446744073709551615", PersonID: "owner", Recovering: recovering.Load,
		Operations: map[protocol.Kind]Operation{
			protocol.KindInput: {Capability: capability, Handle: func(context.Context, Session, protocol.Envelope) (Result, error) {
				mutations.Add(1)
				return Result{Kind: protocol.KindItemDone, Payload: protocol.ItemDone{}, Effect: protocol.EffectApplied}, nil
			}},
			protocol.KindHistoryGet: {Capability: capability, Handle: func(context.Context, Session, protocol.Envelope) (Result, error) {
				return Result{Kind: protocol.KindHistory, Payload: protocol.History{}}, nil
			}},
		},
	}
	conn, ctx := websocketSession(t, identity)
	hello := testHello(t)
	payload := hello.Payload.(protocol.Hello)
	payload.Capabilities = []protocol.Capability{capability}
	hello.Payload = payload
	welcomed := websocketCall(t, ctx, conn, hello)
	if welcomed.Kind != protocol.KindWelcome {
		t.Fatalf("welcome: %+v", welcomed)
	}
	// Compare with the production line session, including capability filtering.
	local, server := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); servePeer(ctx, server, identity, peerIdentity{}) }()
	local.SetDeadline(time.Now().Add(5 * time.Second))
	localWelcome := exchange(t, local, bufio.NewReader(local), hello)
	local.Close()
	<-done
	if !reflect.DeepEqual(welcomed.Payload, localWelcome.Payload) {
		t.Fatal("transport changed welcome")
	}
	req := hello
	req.Epoch, req.Kind, req.Payload = identity.Epoch, protocol.KindInput, protocol.Input{}
	for _, tc := range []struct {
		name string
		edit func(*protocol.Envelope)
		code protocol.ErrorCode
	}{
		{"sender", func(e *protocol.Envelope) { e.Sender.ID = "forged" }, protocol.ErrorNotAuthorized},
		{"epoch", func(e *protocol.Envelope) { e.Epoch = "1" }, protocol.ErrorStaleEpoch},
		{"decider", func(e *protocol.Envelope) {
			e.Kind = protocol.KindQuestionAnswer
			e.Payload = protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline}
		}, protocol.ErrorNotAuthorized},
		{"capability", func(e *protocol.Envelope) { e.Kind = protocol.KindRunningGet; e.Payload = protocol.RunningGet{} }, protocol.ErrorCapabilityNotNegotiated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := req
			tc.edit(&e)
			got := websocketCall(t, ctx, conn, e)
			if got.Error == nil || got.Error.Code != tc.code || got.EffectState != protocol.EffectRefused {
				t.Fatalf("refusal: %+v", got)
			}
		})
	}
	recovering.Store(true)
	got := websocketCall(t, ctx, conn, req)
	if got.Error == nil || got.Error.Code != protocol.ErrorUnavailable {
		t.Fatal("recovery admitted mutation")
	}
	read := req
	read.Kind, read.Payload = protocol.KindHistoryGet, protocol.HistoryGet{}
	if got := websocketCall(t, ctx, conn, read); got.Kind != protocol.KindHistory {
		t.Fatal("recovery refused read")
	}
	if mutations.Load() != 0 {
		t.Fatal("refused request reached handler")
	}
	recovering.Store(false)
	if got := websocketCall(t, ctx, conn, req); got.Kind != protocol.KindItemDone || got.EffectState != protocol.EffectApplied || mutations.Load() != 1 {
		t.Fatal("accepted operation did not execute once")
	}
}

func TestWebSocketDisconnectCancelsOperation(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	capability := protocol.Capability{Name: "conversation", Version: "1.0"}
	conn, ctx := websocketSession(t, Identity{MainID: "main", Epoch: "1", Operations: map[protocol.Kind]Operation{
		protocol.KindHistoryGet: {Capability: capability, Handle: func(ctx context.Context, _ Session, _ protocol.Envelope) (Result, error) {
			close(entered)
			<-ctx.Done()
			close(canceled)
			return Result{}, ctx.Err()
		}},
	}})
	hello := testHello(t)
	payload := hello.Payload.(protocol.Hello)
	payload.Capabilities = []protocol.Capability{capability}
	hello.Payload = payload
	websocketCall(t, ctx, conn, hello)
	hello.Epoch, hello.Kind, hello.Payload = "1", protocol.KindHistoryGet, protocol.HistoryGet{}
	if err := conn.Write(ctx, map[string]any{"jsonrpc": "2.0", "id": "call", "method": hello.Kind, "params": hello}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	conn.Close()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("disconnect did not cancel operation")
	}
}

func clientTransportEndpoint(t *testing.T) (string, context.Context, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err == nil {
			servePeer(ctx, conn, Identity{MainID: "main", Epoch: "1"}, peerIdentity{})
		}
	}()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	return l.Addr().String(), ctx, cancel, done
}

func TestWebSocketUpgradeOriginAndRoute(t *testing.T) {
	for _, tc := range []struct {
		name, path, origin string
		status             int
	}{
		{"native", "/client", "", 101},
		{"same origin", "/client", "same", 101},
		{"foreign origin", "/client", "https://untrusted.invalid", 403},
		{"null origin", "/client", "null", 403},
		{"other route", "/other", "", 404},
		{"query", "/client?identity=owner", "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address, ctx, _, _ := clientTransportEndpoint(t)
			headers := http.Header{}
			if tc.origin == "same" {
				headers.Set("Origin", "http://"+address)
			} else if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			conn, response, err := websocket.Dial(ctx, "ws://"+address+tc.path, &websocket.DialOptions{HTTPHeader: headers})
			if tc.status != 101 {
				if err == nil {
					conn.CloseNow()
					t.Fatal("unexpected upgrade")
				}
				if response == nil || response.StatusCode != tc.status {
					t.Fatalf("refusal: response=%v error=%v", response, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			framed := rpc.NewWebSocket(conn)
			defer framed.Close()
			if websocketCall(t, ctx, framed, testHello(t)).Kind != protocol.KindWelcome {
				t.Fatal("upgrade did not reach Core session")
			}
		})
	}
}

func TestWebSocketShutdownJoinsUpgradeAndSession(t *testing.T) {
	for _, phase := range []string{"sniff", "headers", "upgraded"} {
		t.Run(phase, func(t *testing.T) {
			address, ctx, cancel, done := clientTransportEndpoint(t)
			if phase == "upgraded" {
				conn, _, err := websocket.Dial(ctx, "ws://"+address+"/client", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.CloseNow()
			} else {
				conn, err := net.Dial("tcp", address)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				data := "G"
				if phase == "headers" {
					data = "GET /client HTTP/1.1\r\n"
				}
				if _, err := conn.Write([]byte(data)); err != nil {
					t.Fatal(err)
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Core shutdown left an HTTP/session handler")
			}
		})
	}
}
