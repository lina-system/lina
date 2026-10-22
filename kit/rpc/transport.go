// SPDX-License-Identifier: Apache-2.0

package rpc

import (
	"context"
	"net"
)

// Transport preserves message boundaries independently of physical framing.
// Read is called by one goroutine; Write and Close may run concurrently.
// Close must unblock Read and Write. Owners close on context cancellation.
type Transport interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, any) error
	Close() error
}

type stream struct {
	net.Conn
	reader *Reader
	writer *Writer
}

func NewStream(conn net.Conn) Transport {
	return &stream{Conn: conn, reader: NewReader(conn), writer: NewWriter(conn)}
}

func (s *stream) Read(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.reader.Read()
}

func (s *stream) Write(ctx context.Context, value any) error { return s.writer.Write(ctx, value) }
