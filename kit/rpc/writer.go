// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

var ErrFrameTooLarge = errors.New("RPC frame exceeds limit")

// Writer serializes complete frames. Each physical write is bounded by the
// caller's deadline or the C-42 client timeout, whichever is earlier. A failed
// physical write may be partial; its owner must close the connection.
type Writer struct {
	conn net.Conn
	mu   sync.Mutex
}

func NewWriter(conn net.Conn) *Writer { return &Writer{conn: conn} }

func (w *Writer) Write(ctx context.Context, value any) error {
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
	data = append(data, '\n')
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(45 * time.Second)
	if earlier, ok := ctx.Deadline(); ok && earlier.Before(deadline) {
		deadline = earlier
	}
	if err := w.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	joined := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { w.conn.SetWriteDeadline(time.Now()); close(joined) })
	defer func() {
		if !stop() {
			<-joined
		}
		w.conn.SetWriteDeadline(time.Time{})
	}()
	for len(data) > 0 {
		n, err := w.conn.Write(data)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
