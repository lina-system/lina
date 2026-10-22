// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/kit/protocol/internal/schema"
)

// DecodeStats counts received envelopes, not logical messages. A repeated
// delivery counts again; deduplication belongs to the application layer.
type DecodeStats struct {
	Accepted     uint64 `json:"accepted"`
	Malformed    uint64 `json:"malformed"`
	UnknownKinds uint64 `json:"unknown_kinds"`
}

// ClientDecoder validates incoming envelopes and known payload fields, ignoring
// additive payload fields and unknown kinds. Use one zero-value decoder per
// connection, from that connection's receive loop.
// It does not perform negotiation, framing or application state transitions.
type ClientDecoder struct {
	stats DecodeStats
}

// Common envelope rules remain strict. Known payloads are projected onto this
// client's schema before the strict codec validates and decodes them.
var clientReceiveSchema = receiveShape(clientSchema)
var clientStreamItemSchema = receiveShape(clientSchema.Defs["stream_item"])
var clientPayloadSchemas = func() map[string]*schema.Node {
	result := map[string]*schema.Node{}
	for _, rule := range clientSchema.AllOf {
		if payload := payloadRule(rule); payload != nil {
			var kind string
			if err := json.Unmarshal(rule.If.Properties["kind"].Const, &kind); err != nil {
				panic(err) // The generator requires a scalar kind discriminator.
			}
			result[kind] = payload
		}
	}
	return result
}()

func receiveShape(source *schema.Node) *schema.Node {
	root := *source
	root.Properties = maps.Clone(root.Properties)
	kind := *root.Properties["kind"]
	kind.Enum = nil
	minimum := 1
	kind.MinLength = &minimum
	root.Properties["kind"] = &kind
	root.Properties["payload"] = &schema.Node{Type: "object"}
	root.AllOf = nil
	for _, rule := range source.AllOf {
		if payloadRule(rule) == nil {
			root.AllOf = append(root.AllOf, rule)
		}
	}
	return &root
}

func payloadRule(rule *schema.Node) *schema.Node {
	if rule.If != nil && rule.If.Properties["kind"] != nil && rule.Then != nil {
		return rule.Then.Properties["payload"]
	}
	return nil
}

// Decode returns nil, nil for a well-formed envelope of an unknown kind. Such a
// frame increments UnknownKinds and never becomes conversation content. Invalid
// envelopes increment Malformed and return an error without a partial message.
func (d *ClientDecoder) Decode(data []byte) (*Envelope, error) {
	if err := clientReceiveSchema.Validate(data, clientSchema); err != nil {
		d.stats.Malformed++
		return nil, err
	}
	var wire map[string]any
	// Schema validation above has already checked the JSON and the kind's type.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&wire); err != nil {
		d.stats.Malformed++
		return nil, err
	}
	kind := wire["kind"].(string)
	if !slices.Contains(clientSchema.Properties["kind"].Enum, kind) {
		d.stats.UnknownKinds++
		return nil, nil
	}
	value, err := receiveValue(clientPayloadSchemas[kind], wire["payload"])
	if err != nil {
		d.stats.Malformed++
		return nil, err
	}
	wire["payload"] = value
	data, err = json.Marshal(wire)
	if err != nil {
		d.stats.Malformed++
		return nil, err
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

func validateValue(n *schema.Node, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return n.Validate(data, clientSchema)
}

// receiveValue removes only fields absent from closed payload objects. Required
// fields, known enum values, types and conditional rules are still checked by
// the strict codec. Maps and slices are copied so union alternatives cannot
// change each other's input. Schema-defined maps retain their dynamic keys.
func receiveValue(n *schema.Node, value any) (any, error) {
	var err error
	if n.Ref != "" {
		value, err = receiveValue(clientSchema.Defs[strings.TrimPrefix(n.Ref, "#/$defs/")], value)
		if err != nil {
			return nil, err
		}
	}
	if object, ok := value.(map[string]any); ok {
		object = maps.Clone(object)
		for key, field := range object {
			child := n.Properties[key]
			if child == nil && n.AdditionalProperties != nil {
				child = n.AdditionalProperties.Schema
				if child == nil && !n.AdditionalProperties.Allowed {
					delete(object, key)
				}
			}
			if child != nil {
				object[key], err = receiveValue(child, field)
				if err != nil {
					return nil, err
				}
			}
		}
		value = object
	}
	if array, ok := value.([]any); ok && n.Items != nil {
		items := make([]any, 0, len(array))
		for _, item := range array {
			if n.Items.Ref == "#/$defs/stream_item" {
				// Validate the common item shape even when its kind is unknown.
				// Skipped items do not change the snapshot cursor or frame counts.
				if err := validateValue(clientStreamItemSchema, item); err != nil {
					return nil, err
				}
				kind := item.(map[string]any)["kind"].(string)
				if !slices.Contains(clientSchema.Defs["stream_item"].Properties["kind"].Enum, kind) {
					continue
				}
			}
			item, err = receiveValue(n.Items, item)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}
		value = items
	}
	for _, alternatives := range [][]*schema.Node{n.AnyOf, n.OneOf} {
		if len(alternatives) == 0 {
			continue
		}
		matched := false
		for _, child := range alternatives {
			candidate, err := receiveValue(child, value)
			if err == nil && validateValue(child, candidate) == nil {
				value, matched = candidate, true
				break
			}
		}
		if !matched {
			return nil, fmt.Errorf("payload has no valid union variant")
		}
	}
	for _, child := range n.AllOf {
		value, err = receiveValue(child, value)
		if err != nil {
			return nil, err
		}
	}
	if n.If != nil {
		child := n.Else
		if validateValue(n.If, value) == nil {
			child = n.Then
		}
		if child != nil {
			return receiveValue(child, value)
		}
	}
	return value, nil
}
