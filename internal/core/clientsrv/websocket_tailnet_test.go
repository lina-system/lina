// SPDX-License-Identifier: Apache-2.0

package clientsrv

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

// Only physical addressing and the local daemon are synthetic. Serve performs
// its real owner admission before HTTP upgrade, and rechecks decision authority.
type websocketTailnetListener struct{ net.Listener }

func (l websocketTailnetListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return websocketTailnetConn{c}, nil
}

type websocketTailnetConn struct{ net.Conn }

func (c websocketTailnetConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("100.100.0.2"), Port: 1234}
}

func TestWebSocketTailnetAuthenticatesBeforeUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var owner atomic.Uint64
	owner.Store(7)
	apiSocket := filepath.Join(t.TempDir(), "tailscaled.sock")
	apiListener, err := net.Listen("unix", apiSocket)
	if err != nil {
		t.Fatal(err)
	}
	api := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/localapi/v0/status":
			json.NewEncoder(w).Encode(map[string]any{"BackendState": "Running", "TailscaleIPs": []string{"100.100.0.1"}, "Self": map[string]any{"ID": "self", "UserID": 7}})
		case "/localapi/v0/whois":
			if r.URL.Query().Get("addr") != "100.100.0.2:1234" {
				t.Error("lookup used untrusted HTTP identity")
			}
			json.NewEncoder(w).Encode(map[string]any{"Node": map[string]any{"StableID": "remote", "User": owner.Load(), "Addresses": []string{"100.100.0.2/32"}}, "UserProfile": map[string]any{"ID": owner.Load()}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})}
	apiDone := make(chan struct{})
	go func() { defer close(apiDone); api.Serve(apiListener) }()
	t.Cleanup(func() { api.Close(); <-apiDone })
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	listener, err := ListenTailnet(ctx, TailnetConfig{Address: "100.100.0.1:9000", Socket: apiSocket, Listen: func(_ context.Context, network, address string) (net.Listener, error) {
		if network != "tcp" || address != "100.100.0.1:9000" {
			t.Error("incorrect requested bind")
		}
		return websocketTailnetListener{raw}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	var decisions atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, listener, Identity{MainID: "main", Epoch: "1", PersonID: "owner", Operations: map[protocol.Kind]Operation{
			protocol.KindQuestionAnswer: {Capability: DecisionsCapability, Handle: func(_ context.Context, s Session, _ protocol.Envelope) (Result, error) {
				if s.PersonID != "owner" || !s.CanDecide {
					t.Error("remote session lost authenticated owner")
				}
				decisions.Add(1)
				return Result{Kind: protocol.KindQuestionAnswer, Payload: protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline}, Effect: protocol.EffectApplied}, nil
			}},
		}})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	endpoint := "ws://" + raw.Addr().String() + "/client"
	wire, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := rpc.NewWebSocket(wire)
	defer conn.Close()
	hello := testHello(t)
	payload := hello.Payload.(protocol.Hello)
	payload.Capabilities = []protocol.Capability{DecisionsCapability}
	hello.Payload = payload
	if websocketCall(t, ctx, conn, hello).Kind != protocol.KindWelcome {
		t.Fatal("owner not welcomed")
	}
	req := hello
	req.Epoch = "1"
	req.Kind = protocol.KindQuestionAnswer
	req.Payload = protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline}
	if got := websocketCall(t, ctx, conn, req); got.EffectState != protocol.EffectApplied || decisions.Load() != 1 {
		t.Fatal("owner decision not dispatched")
	}
	owner.Store(8)
	if got := websocketCall(t, ctx, conn, req); got.Error == nil || got.Error.Code != protocol.ErrorNotAuthorized || decisions.Load() != 1 {
		t.Fatal("revoked WebSocket retained decision authority")
	}
	for _, headers := range []http.Header{nil, {"X-Forwarded-For": []string{"100.100.0.1"}, "Tailscale-User-Login": []string{"owner"}}} {
		unauthorized, response, err := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPHeader: headers})
		if err == nil {
			unauthorized.CloseNow()
			t.Fatal("non-owner upgraded")
		}
		if response != nil && response.StatusCode == http.StatusSwitchingProtocols {
			t.Fatal("authentication occurred after upgrade")
		}
	}
}
