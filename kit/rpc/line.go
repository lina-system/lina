// SPDX-License-Identifier: Apache-2.0

// Package rpc provides bounded newline framing for JSON-RPC streams.
package rpc

import (
	"bufio"
	"io"
)

const MaxFrameBytes = 1 << 20

// Reader preserves buffered frames between requests. A frame includes its
// terminating newline; EOF without it is never a complete message.
type Reader struct{ reader *bufio.Reader }

func NewReader(r io.Reader) *Reader { return &Reader{bufio.NewReaderSize(r, MaxFrameBytes)} }

// Read returns a frame valid until the next Read. Allocation is bounded even
// when a peer never sends a newline. An oversized or partial frame is an error.
func (r *Reader) Read() ([]byte, error) { return r.reader.ReadSlice('\n') }
