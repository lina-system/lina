// SPDX-License-Identifier: Apache-2.0

// Package clientapi implements the Core side of the client protocol boundary.
package clientapi

import (
	"encoding/json"

	"github.com/thisisjun786/lina/kit/protocol"
)

// RequestDecoder checks one unframed request envelope at a time. Its zero value
// is ready for one connection's receive loop. Handshake, authorization, routing
// and response framing belong to the client service above this boundary.
type RequestDecoder struct {
	stats protocol.DecodeStats
}

// Decode refuses unknown fields and kinds with the malformed code, before a
// request can be dispatched. A refusal never returns a partial request.
func (d *RequestDecoder) Decode(data []byte) (*protocol.Envelope, *protocol.Error) {
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		d.stats.Malformed++
		return nil, &protocol.Error{Code: protocol.ErrorMalformed, Detail: err.Error()}
	}
	d.stats.Accepted++
	return &envelope, nil
}

func (d *RequestDecoder) Stats() protocol.DecodeStats { return d.stats }
