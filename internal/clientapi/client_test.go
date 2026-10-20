// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

type testRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      string            `json:"id"`
	Method  string            `json:"method"`
	Params  protocol.Envelope `json:"params"`
}

type testResponse struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      string            `json:"id"`
	Result  protocol.Envelope `json:"result"`
}

func responseFixture(t *testing.T, name string) protocol.Envelope {
	t.Helper()
	data, err := os.ReadFile("../../protocol/fixtures/client/1.0/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func peer(t *testing.T, respond func(testRequest) []byte) (string, <-chan testRequest, <-chan error) {
	t.Helper()
	root, err := os.MkdirTemp("", "lina-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	socket := filepath.Join(root, "core.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan testRequest, 1)
	done := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		reader := bufio.NewReader(conn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var request testRequest
		if err := json.Unmarshal(line, &request); err != nil {
			done <- err
			return
		}
		requests <- request
		data := respond(request)
		if data == nil {
			done <- nil
			return
		}
		// An empty response waits for client closure without writing. Even a
		// zero-length write can fail if cancellation already closed the peer.
		if len(data) > 0 {
			if _, err := conn.Write(data); err != nil {
				done <- err
				return
			}
		}
		// The client must close on failure or Close; it sends no operations.
		_, err = reader.ReadByte()
		if !errors.Is(err, io.EOF) {
			done <- errors.New("client did not close without further requests")
			return
		}
		done <- nil
	}()
	t.Cleanup(func() { listener.Close(); <-finished })
	return socket, requests, done
}

func TestDialWelcome(t *testing.T) {
	welcome := responseFixture(t, "welcome")
	socket, requests, done := peer(t, func(request testRequest) []byte {
		welcome.ReplyTo = &request.Params.MessageID
		data, _ := json.Marshal(testResponse{"2.0", request.ID, welcome})
		return append(data, '\n')
	})
	client, err := Dial(context.Background(), socket, "0.0.1-alpha.dev")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := <-requests
	hello, ok := request.Params.Payload.(protocol.Hello)
	if request.JSONRPC != "2.0" || request.Method != "hello" || request.ID == "" ||
		request.Params.Kind != protocol.KindHello || request.Params.Sender.Party != protocol.PartyClient ||
		!ok || hello.Party != protocol.PartyClient || hello.ID != request.Params.Sender.ID ||
		hello.ProductVersion != "0.0.1-alpha.dev" || !reflect.DeepEqual(hello.ProtocolVersions, []string{"1.0"}) || len(hello.Capabilities) != 0 {
		t.Fatalf("invalid hello: %#v", request)
	}
	if !reflect.DeepEqual(client.Welcome, welcome.Payload) {
		t.Fatalf("welcome = %#v", client.Welcome)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDialRefusal(t *testing.T) {
	refusal := responseFixture(t, "unsupported_combination")
	socket, _, done := peer(t, func(request testRequest) []byte {
		refusal.ReplyTo = &request.Params.MessageID
		data, _ := json.Marshal(testResponse{"2.0", request.ID, refusal})
		return append(data, '\n')
	})
	client, err := Dial(context.Background(), socket, "0.0.1-alpha.dev")
	var refused *Refusal
	if client != nil || !errors.As(err, &refused) || refused.Cause != *refusal.Error {
		t.Fatalf("refusal = %v, %v", client, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidHandshake(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testResponse)
	}{
		{"rpc version", func(r *testResponse) { r.JSONRPC = "1.0" }},
		{"rpc id", func(r *testResponse) { r.ID = "unrelated" }},
		{"reply id", func(r *testResponse) { value := "unrelated"; r.Result.ReplyTo = &value }},
		{"missing reply id", func(r *testResponse) { r.Result.ReplyTo = nil }},
		{"sender party", func(r *testResponse) { r.Result.Sender.Party = protocol.PartyClient }},
		{"main id", func(r *testResponse) { r.Result.Sender.ID = "unrelated" }},
		{"epoch", func(r *testResponse) { r.Result.Epoch = "43" }},
		{"selected version", func(r *testResponse) {
			w := r.Result.Payload.(protocol.Welcome)
			w.ProtocolVersion = "2.0"
			r.Result.Payload = w
		}},
		{"unoffered capability", func(r *testResponse) {
			w := r.Result.Payload.(protocol.Welcome)
			w.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
			r.Result.Payload = w
		}},
		{"welcome effect", func(r *testResponse) { r.Result.EffectState = protocol.EffectFailed }},
		{"unexpected kind", func(r *testResponse) { r.Result.Kind = protocol.KindConnected; r.Result.Payload = protocol.Connected{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			welcome := responseFixture(t, "welcome")
			socket, _, done := peer(t, func(request testRequest) []byte {
				welcome.ReplyTo = &request.Params.MessageID
				response := testResponse{"2.0", request.ID, welcome}
				tc.mutate(&response)
				// A faulty peer can send data that our generated encoder would
				// reject. Bypass that encoder to exercise the client's decoder.
				type rawEnvelope protocol.Envelope
				data, err := json.Marshal(struct {
					JSONRPC string      `json:"jsonrpc"`
					ID      string      `json:"id"`
					Result  rawEnvelope `json:"result"`
				}{response.JSONRPC, response.ID, rawEnvelope(response.Result)})
				if err != nil {
					t.Errorf("encode test response: %v", err)
					return nil
				}
				return append(data, '\n')
			})
			if client, err := Dial(context.Background(), socket, "0.0.1-alpha.dev"); err == nil {
				client.Close()
				t.Fatal("accepted invalid handshake")
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectBrokenFrames(t *testing.T) {
	for _, frame := range []string{"{\n", "{}\n", strings.Repeat("x", maxFrameBytes)} {
		t.Run(stringLength(frame), func(t *testing.T) {
			socket, _, done := peer(t, func(testRequest) []byte { return []byte(frame) })
			if client, err := Dial(context.Background(), socket, "0.0.1-alpha.dev"); err == nil {
				client.Close()
				t.Fatal("accepted broken frame")
			}
			// A peer writing an oversized frame may observe the client's reset.
			<-done
		})
	}
}

func stringLength(s string) string {
	if len(s) > 100 {
		return "oversized"
	}
	return s
}

func TestHandshakeEOF(t *testing.T) {
	socket, _, done := peer(t, func(testRequest) []byte { return nil })
	if client, err := Dial(context.Background(), socket, "0.0.1-alpha.dev"); client != nil || !errors.Is(err, io.EOF) {
		t.Fatalf("handshake EOF = %v, %v", client, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestDialCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	socket, requests, done := peer(t, func(testRequest) []byte { return []byte{} })
	result := make(chan error, 1)
	go func() {
		client, err := Dial(ctx, socket, "0.0.1-alpha.dev")
		if client != nil {
			client.Close()
		}
		result <- err
	}()
	select {
	case <-requests:
	case err := <-result:
		t.Fatalf("handshake ended before cancellation: %v", err)
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("canceled handshake succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not interrupt handshake")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
