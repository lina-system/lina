// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/thisisjun786/lina/kit/protocol/internal/schema"
)

// DecodeStats counts received envelopes, not logical messages. A repeated
// delivery counts again; deduplication belongs to the application layer.
type DecodeStats struct {
	Accepted     uint64 `json:"accepted"`
	Malformed    uint64 `json:"malformed"`
	UnknownKinds uint64 `json:"unknown_kinds"`
}

// ClientDecoder validates incoming envelopes and ignores unknown kinds. Use one
// zero-value decoder per connection, from that connection's receive loop.
// It does not perform negotiation, framing or application state transitions.
type ClientDecoder struct {
	stats DecodeStats
}

// Only the kind's closed enumeration is relaxed. All common envelope rules,
// including required fields, version and effect/error constraints, still apply.
var clientReceiveSchema = func() *schema.Node {
	root := *clientSchema
	root.Properties = maps.Clone(root.Properties)
	kind := *root.Properties["kind"]
	kind.Enum = nil
	minimum := 1
	kind.MinLength = &minimum
	root.Properties["kind"] = &kind
	return &root
}()

// Decode returns nil, nil for a well-formed envelope of an unknown kind. Such a
// frame increments UnknownKinds and never becomes conversation content. Invalid
// envelopes increment Malformed and return an error without a partial message.
func (d *ClientDecoder) Decode(data []byte) (*Envelope, error) {
	if err := clientReceiveSchema.Validate(data, clientReceiveSchema); err != nil {
		d.stats.Malformed++
		return nil, err
	}
	var header struct {
		Kind string `json:"kind"`
	}
	// Schema validation above has already checked the JSON and the kind's type.
	if err := json.Unmarshal(data, &header); err != nil {
		d.stats.Malformed++
		return nil, err
	}
	if !slices.Contains(clientSchema.Properties["kind"].Enum, header.Kind) {
		d.stats.UnknownKinds++
		return nil, nil
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		d.stats.Malformed++
		return nil, err
	}
	d.stats.Accepted++
	return &envelope, nil
}

func (d *ClientDecoder) Stats() DecodeStats { return d.stats }
