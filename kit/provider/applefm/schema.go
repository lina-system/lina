// SPDX-License-Identifier: Apache-2.0

package applefm

import (
	"bytes"
	"encoding/json"
	"errors"
)

// ErrSchemaMiss means guided generation did not return an object that matches
// the schema. Callers must not fill the missing fields.
var ErrSchemaMiss = errors.New("schema_miss")

type fieldSchema struct {
	Type string `json:"type"`
}

type objectSchema struct {
	Type                 string                 `json:"type"`
	Properties           map[string]fieldSchema `json:"properties"`
	Required             []string               `json:"required"`
	AdditionalProperties *bool                  `json:"additionalProperties"`
}

// ValidSchema accepts a closed object of strings, integers and booleans.
func ValidSchema(raw json.RawMessage) bool {
	var schema objectSchema
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&schema); err != nil || dec.More() {
		return false
	}
	if schema.Type != "object" || schema.AdditionalProperties == nil || *schema.AdditionalProperties || schema.Properties == nil || len(schema.Required) != len(schema.Properties) {
		return false
	}
	seen := map[string]bool{}
	for _, name := range schema.Required {
		field, ok := schema.Properties[name]
		if !ok || seen[name] || !knownType(field.Type) {
			return false
		}
		seen[name] = true
	}
	for name, field := range schema.Properties {
		if name == "" || !knownType(field.Type) {
			return false
		}
	}
	return true
}

func knownType(kind string) bool {
	return kind == "string" || kind == "integer" || kind == "boolean"
}

// ValidateObject checks raw against schema and returns the compact object.
func ValidateObject(schema, raw json.RawMessage) (json.RawMessage, error) {
	if !ValidSchema(schema) || !json.Valid(raw) {
		return nil, ErrSchemaMiss
	}
	var spec objectSchema
	if err := json.Unmarshal(schema, &spec); err != nil {
		return nil, ErrSchemaMiss
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || dec.More() || fields == nil {
		return nil, ErrSchemaMiss
	}
	if len(fields) != len(spec.Properties) {
		return nil, ErrSchemaMiss
	}
	for name, field := range spec.Properties {
		value, ok := fields[name]
		if !ok || !valueMatches(field.Type, value) {
			return nil, ErrSchemaMiss
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, ErrSchemaMiss
	}
	return compact.Bytes(), nil
}

func valueMatches(kind string, raw json.RawMessage) bool {
	switch kind {
	case "string":
		var text string
		return json.Unmarshal(raw, &text) == nil && len(raw) > 0 && raw[0] == '"'
	case "boolean":
		var value bool
		return json.Unmarshal(raw, &value) == nil && (bytes.Equal(raw, []byte("true")) || bytes.Equal(raw, []byte("false")))
	case "integer":
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] == '"' {
			return false
		}
		var number json.Number
		if json.Unmarshal(raw, &number) != nil {
			return false
		}
		text := number.String()
		if text == "" || text == "-" {
			return false
		}
		digits := text
		if text[0] == '-' {
			digits = text[1:]
		}
		if digits == "" {
			return false
		}
		for _, c := range digits {
			if c < '0' || c > '9' {
				return false
			}
		}
		return true
	default:
		return false
	}
}
