// SPDX-License-Identifier: Apache-2.0
package textcall

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"reflect"
	"unicode/utf8"
)

var errSyntax = errors.New("invalid call")

// decodeJSON rejects duplicate keys instead of silently executing the last one.
func decodeJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errSyntax
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	v, err := jsonValue(d, 0)
	if err != nil {
		return nil, err
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, errSyntax
	}
	return v, nil
}
func jsonValue(d *json.Decoder, depth int) (any, error) {
	if depth > 64 {
		return nil, errSyntax
	}
	token, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch token {
	case json.Delim('{'):
		out := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			name, ok := key.(string)
			if !ok {
				return nil, errSyntax
			}
			if _, exists := out[name]; exists {
				return nil, errSyntax
			}
			value, err := jsonValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errSyntax
		}
		return out, nil
	case json.Delim('['):
		out := []any{}
		for d.More() {
			value, err := jsonValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errSyntax
		}
		return out, nil
	default:
		if _, ok := token.(json.Delim); ok {
			return nil, errSyntax
		}
		return token, nil
	}
}
func equalCalls(a, b *Call) bool {
	if a == nil || b == nil {
		return a == b
	}
	av, ae := decodeJSON(a.Arguments)
	bv, be := decodeJSON(b.Arguments)
	return a.Name == b.Name && ae == nil && be == nil && equalJSON(av, bv)
}
func equalJSON(a, b any) bool {
	switch a := a.(type) {
	case json.Number:
		b, ok := b.(json.Number)
		if !ok {
			return false
		}
		ar, ao := new(big.Rat).SetString(string(a))
		br, bo := new(big.Rat).SetString(string(b))
		return ao && bo && ar.Cmp(br) == 0
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for k, v := range a {
			w, ok := b[k]
			if !ok || !equalJSON(v, w) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i, v := range a {
			if !equalJSON(v, b[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
