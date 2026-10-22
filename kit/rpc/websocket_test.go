// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/protocol/conformance"
)

func socketPair(t *testing.T) (*WebSocket, *websocket.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	accepted := make(chan *WebSocket, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- NewWebSocket(conn)
	}))
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		server.Close()
		cancel()
		t.Fatal(err)
	}
	client.SetReadLimit(MaxFrameBytes)
	var framed *WebSocket
	select {
	case framed = <-accepted:
	case <-ctx.Done():
		client.CloseNow()
		server.Close()
		cancel()
		t.Fatal(ctx.Err())
	}
	t.Cleanup(func() { client.CloseNow(); framed.Close(); server.Close(); cancel() })
	return framed, client, ctx
}

func TestWebSocketClientConformance(t *testing.T) {
	suite, err := conformance.Load(os.DirFS("../../protocol/fixtures/client/1.0"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range suite.Cases {
		if c.Side != "client" {
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			server, client, ctx := socketPair(t)
			var decoder protocol.ClientDecoder
			err := suite.Run(c, conformance.Receiver{
				Decode: func(data []byte) (json.RawMessage, protocol.ErrorCode, error) {
					// The outer JSON-RPC object is unchanged on the wire.
					if err := server.Write(ctx, struct {
						JSONRPC string          `json:"jsonrpc"`
						Method  string          `json:"method"`
						Params  json.RawMessage `json:"params"`
					}{"2.0", "fixture", data}); err != nil {
						return nil, "", err
					}
					typ, wire, err := client.Read(ctx)
					if err != nil {
						return nil, "", err
					}
					if typ != websocket.MessageText || bytes.ContainsAny(wire, "\r\n") {
						return nil, "", errors.New("not one text message")
					}
					var msg struct {
						Params json.RawMessage `json:"params"`
					}
					if err := json.Unmarshal(wire, &msg); err != nil {
						return nil, "", err
					}
					envelope, err := decoder.Decode(msg.Params)
					if err != nil {
						return nil, protocol.ErrorMalformed, nil
					}
					if envelope == nil {
						return nil, "", nil
					}
					out, err := json.Marshal(envelope)
					return out, "", err
				}, Stats: decoder.Stats,
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
	// Every valid envelope also crosses the opposite direction without loss.
	server, client, ctx := socketPair(t)
	if err := suite.RoundTrip(func(data []byte) ([]byte, error) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			return nil, err
		}
		wire := append([]byte(`{"jsonrpc":"2.0","id":"fixture","result":`), compact.Bytes()...)
		wire = append(wire, '}')
		if err := client.Write(ctx, websocket.MessageText, wire); err != nil {
			return nil, err
		}
		got, err := server.Read(ctx)
		if err != nil {
			return nil, err
		}
		var msg struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(got, &msg); err != nil {
			return nil, err
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(msg.Result, &envelope); err != nil {
			return nil, err
		}
		return json.Marshal(envelope)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestWebSocketMessageBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		typ        websocket.MessageType
		valid      bool
	}{
		{"object", `{"jsonrpc":"2.0"}`, websocket.MessageText, true},
		{"escaped newline", `{"text":"a\nb"}`, websocket.MessageText, true},
		{"maximum", `{"v":"` + strings.Repeat("x", MaxFrameBytes-9) + `"}`, websocket.MessageText, true},
		{"oversized", `{"v":"` + strings.Repeat("x", MaxFrameBytes) + `"}`, websocket.MessageText, false},
		{"binary", `{}`, websocket.MessageBinary, false},
		{"newline", "{}\n", websocket.MessageText, false},
		{"joined", `{} {}`, websocket.MessageText, false},
		{"batch", `[{}]`, websocket.MessageText, false},
		{"partial", `{`, websocket.MessageText, false},
		{"utf8", "{\"v\":\"\xff\"}", websocket.MessageText, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client, ctx := socketPair(t)
			written := make(chan error, 1)
			go func() { written <- client.Write(ctx, tc.typ, []byte(tc.data)) }()
			got, err := server.Read(ctx)
			if (err == nil) != tc.valid {
				t.Fatalf("Read: bytes=%d error=%v", len(got), err)
			}
			if tc.valid && string(got) != tc.data {
				t.Fatal("message changed")
			}
			server.Close()
			<-written
		})
	}
}

func TestWebSocketRejectedWriteAndCancellation(t *testing.T) {
	server, client, ctx := socketPair(t)
	if err := server.Write(ctx, map[string]string{"v": strings.Repeat("x", MaxFrameBytes)}); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatal(err)
	}
	if err := server.Write(ctx, map[string]bool{"ready": true}); err != nil {
		t.Fatal(err)
	}
	_, got, err := client.Read(ctx)
	if err != nil || string(got) != `{"ready":true}` {
		t.Fatalf("read after rejected write: %s %v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := server.Read(canceled); err == nil {
		t.Fatal("canceled read succeeded")
	}
}

func TestWebSocketFragmentedTextAndPing(t *testing.T) {
	server, client, ctx := socketPair(t)
	want := `{"text":"` + strings.Repeat("x", 12000) + `"}`
	result := make(chan []byte, 1)
	failure := make(chan error, 1)
	go func() { data, err := server.Read(ctx); result <- data; failure <- err }()
	writer, err := client.Writer(ctx, websocket.MessageText)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(want[:6000])); err != nil {
		t.Fatal(err)
	}
	// Ping's matching pong is processed by the client read loop.
	readCtx, cancel := context.WithCancel(ctx)
	readDone := make(chan struct{})
	go func() { defer close(readDone); client.Read(readCtx) }()
	if err := client.Ping(ctx); err != nil {
		cancel()
		<-readDone
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(want[6000:])); err != nil {
		cancel()
		<-readDone
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		cancel()
		<-readDone
		t.Fatal(err)
	}
	if got, err := <-result, <-failure; err != nil || string(got) != want {
		t.Fatalf("fragmented message: bytes=%d error=%v", len(got), err)
	}
	cancel()
	<-readDone
}
