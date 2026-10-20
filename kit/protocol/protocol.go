// SPDX-License-Identifier: Apache-2.0

// Package protocol provides generated client 1.0 types and a strict JSON codec.
// Reserved payloads name future implementation work; accepting their empty
// shape does not implement that capability. Framing and negotiation live above
// this package.
package protocol

//go:generate go run ./internal/generate

import (
	"encoding/json"

	"github.com/thisisjun786/lina/kit/protocol/internal/schema"
)

var clientSchema = func() *schema.Node {
	value, err := schema.Parse([]byte(clientSchemaJSON))
	if err != nil {
		panic(err)
	} // Generated schema is checked before it is emitted.
	return value
}()

func (e *Envelope) UnmarshalJSON(data []byte) error {
	if err := clientSchema.Validate(data, clientSchema); err != nil {
		return err
	}
	decoded, err := decodeMessage(data)
	if err != nil {
		return err
	}
	*e = decoded
	return nil
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(message[Payload](e))
	if err != nil {
		return nil, err
	}
	if err := clientSchema.Validate(data, clientSchema); err != nil {
		return nil, err
	}
	return data, nil
}
