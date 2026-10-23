// SPDX-License-Identifier: Apache-2.0

// Package protocol provides generated client 1.0 types and a strict JSON codec.
// Reserved payloads name future implementation work; accepting their empty
// shape does not implement that capability. Framing and negotiation live above
// this package.
package protocol

//go:generate go run ./internal/generate

import (
	"encoding/json"
	"strings"

	"github.com/thisisjun786/lina/kit/protocol/internal/schema"
)

// RefusedFormFieldName reports names the waiting-card bridge will not present.
// A form that uses one cannot be answered, so callers decline it instead of
// leaving the worker waiting.
func RefusedFormFieldName(name string) bool {
	name = strings.ToLower(strings.NewReplacer("-", "_", " ", "_").Replace(name))
	switch name {
	case "password", "passwd", "secret", "api_key", "apikey", "key", "token", "access_token", "refresh_token", "credit_card", "card_number", "cvv", "payment", "payment_details":
		return true
	}
	return name == ""
}

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
