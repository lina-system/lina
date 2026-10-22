// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

var ErrWebSocketMessage = errors.New("expected one JSON object in a WebSocket text message")

// WebSocket carries the same JSON-RPC objects as the line transport, without
// delimiters. One reader and concurrent writers are supported. Closing unblocks
// all I/O; connection owners cancel their operation contexts on read failure.
type WebSocket struct{ conn *websocket.Conn }

func NewWebSocket(conn *websocket.Conn) *WebSocket {
	conn.SetReadLimit(MaxFrameBytes - 1)
	return &WebSocket{conn: conn}
}

func (w *WebSocket) Read(ctx context.Context) ([]byte, error) {
	typ, data, err := w.conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText || !validTextObject(data) {
		w.Close()
		return nil, ErrWebSocketMessage
	}
	return data, nil
}

func validTextObject(data []byte) bool {
	trimmed := bytes.TrimSpace(data)
	return len(trimmed) > 0 && trimmed[0] == '{' && utf8.Valid(data) && !bytes.ContainsAny(data, "\r\n") && json.Valid(data)
}

func (w *WebSocket) Write(ctx context.Context, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) >= MaxFrameBytes {
		return ErrFrameTooLarge
	}
	if !validTextObject(data) {
		return ErrWebSocketMessage
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	return w.conn.Write(ctx, websocket.MessageText, data)
}

// Close never waits for a peer's closing handshake during Core shutdown.
func (w *WebSocket) Close() error { return w.conn.CloseNow() }
