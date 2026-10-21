// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

func TestReceiveKeepsCoalescedHandshakeBytes(t *testing.T) {
	welcome := responseFixture(t, "welcome")
	notice := responseFixture(t, "streams/item_done")
	socket, _, done := peer(t, func(request testRequest) []byte {
		welcome.ReplyTo = &request.Params.MessageID
		first, _ := json.Marshal(testResponse{"2.0", request.ID, welcome})
		second, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": notice.Kind, "params": notice})
		return append(append(append(first, '\n'), second...), '\n')
	})
	client, err := Dial(t.Context(), socket, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	frame, err := client.Receive(t.Context())
	if err != nil || frame.Kind != protocol.KindItemDone {
		t.Fatalf("buffered notification lost: %s %v", frame.Kind, err)
	}
	client.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReceiveRejectsInvalidSessionFrames(t *testing.T) {
	for _, change := range []string{"identity", "epoch", "notification reply", "unknown response", "trailing value"} {
		t.Run(change, func(t *testing.T) {
			welcome := responseFixture(t, "welcome")
			notice := responseFixture(t, "streams/item_done")
			socket, _, done := peer(t, func(request testRequest) []byte {
				welcome.ReplyTo = &request.Params.MessageID
				first, _ := json.Marshal(testResponse{"2.0", request.ID, welcome})
				switch change {
				case "identity":
					notice.Sender.ID = "other"
				case "epoch":
					notice.Epoch = "43"
				case "notification reply":
					s := "unexpected"
					notice.ReplyTo = &s
				}
				frame := map[string]any{"jsonrpc": "2.0", "method": notice.Kind, "params": notice}
				if change == "unknown response" {
					frame = map[string]any{"jsonrpc": "2.0", "id": "unexpected", "result": notice}
				}
				second, _ := json.Marshal(frame)
				if change == "trailing value" {
					second = append(second, []byte(` {}`)...)
				}
				return append(append(append(first, '\n'), second...), '\n')
			})
			client, err := Dial(t.Context(), socket, "test")
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.Receive(t.Context()); !errors.Is(err, ErrMalformed) {
				t.Fatalf("admitted %s: %v", change, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

type observedRead struct {
	net.Conn
	entered chan struct{}
}

func (c observedRead) Read(data []byte) (int, error) { close(c.entered); return c.Conn.Read(data) }

func TestReceiveCancellationClosesBlockedRead(t *testing.T) {
	conn, other := net.Pipe()
	defer other.Close()
	entered := make(chan struct{})
	client := &Client{conn: conn, reader: rpc.NewReader(observedRead{conn, entered})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.Receive(ctx); done <- err }()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked receive: %v", err)
	}
}
