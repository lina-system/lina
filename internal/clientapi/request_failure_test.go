// SPDX-License-Identifier: Apache-2.0

package clientapi

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
	"github.com/thisisjun786/lina/kit/rpc"
)

type failedRequestConn struct {
	net.Conn
	failure error
	writes  int
	closes  int
}

func (c *failedRequestConn) Write([]byte) (int, error) {
	c.writes++
	return 0, c.failure
}

func (c *failedRequestConn) Close() error {
	c.closes++
	return c.Conn.Close()
}

func TestRequestDisconnectClassification(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failure      error
		want         error
		disconnected bool
	}{
		{"broken pipe", &net.OpError{Op: "write", Net: "unix", Err: syscall.EPIPE}, syscall.EPIPE, true},
		{"closed socket", net.ErrClosed, net.ErrClosed, true},
		{"closed pipe", io.ErrClosedPipe, io.ErrClosedPipe, true},
		{"EOF", io.EOF, io.EOF, true},
		{"short write", nil, io.ErrShortWrite, true},
		{"canceled", nil, context.Canceled, false},
		{"oversized", nil, rpc.ErrFrameTooLarge, false},
		{"invalid payload", nil, nil, false},
		{"pending limit", nil, ErrPendingLimit, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local, remote := net.Pipe()
			defer local.Close()
			defer remote.Close()
			conn := &failedRequestConn{Conn: local, failure: tc.failure}
			client := &Client{conn: conn, Welcome: protocol.Welcome{MainID: "main", Epoch: "1", ProtocolVersion: "1.0"}, clientID: "client", writer: rpc.NewWriter(conn), pending: make(map[string]struct{})}
			ctx := t.Context()
			text := "synthetic draft"
			conversation := "main:synthetic"
			if tc.name == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tc.name == "oversized" {
				conversation = strings.Repeat("x", rpc.MaxFrameBytes)
			}
			if tc.name == "invalid payload" {
				text = ""
			}
			if tc.name == "pending limit" {
				for i := range 64 {
					client.pending[string(rune(i))] = struct{}{}
				}
			}
			id, err := client.Input(ctx, protocol.Input{Text: &text, ConversationID: &conversation})
			if id != "" || err == nil || tc.want != nil && !errors.Is(err, tc.want) || errors.Is(err, ErrDisconnected) != tc.disconnected {
				t.Fatalf("id=%q, error=%v; want cause=%v, disconnected=%t", id, err, tc.want, tc.disconnected)
			}
			if tc.disconnected && (conn.writes != 1 || conn.closes != 1 || len(client.pending) != 0) {
				t.Fatalf("failed request replayed or retained: writes=%d closes=%d pending=%d", conn.writes, conn.closes, len(client.pending))
			}
			if !tc.disconnected && conn.writes != 0 {
				t.Fatal("local rejection reached transport")
			}
		})
	}
}
