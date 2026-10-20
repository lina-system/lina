// SPDX-License-Identifier: Apache-2.0

// Package schema implements the schema vocabulary used by the client codec and
// generator. Unsupported keywords fail loading rather than silently weakening
// validation. It is not a general-purpose JSON Schema implementation.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Additional struct {
	Allowed bool
	Schema  *Node
}

func (a *Additional) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("true")) {
		a.Allowed = true
		return nil
	}
	if bytes.Equal(data, []byte("false")) {
		a.Allowed = false
		return nil
	}
	a.Schema = new(Node)
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(a.Schema)
}

type Node struct {
	Dialect              string           `json:"$schema,omitempty"`
	Comment              string           `json:"$comment,omitempty"`
	Title                string           `json:"title,omitempty"`
	Description          string           `json:"description,omitempty"`
	Ref                  string           `json:"$ref,omitempty"`
	Type                 string           `json:"type,omitempty"`
	Const                json.RawMessage  `json:"const,omitempty"`
	Enum                 []string         `json:"enum,omitempty"`
	Properties           map[string]*Node `json:"properties,omitempty"`
	Required             []string         `json:"required,omitempty"`
	AdditionalProperties *Additional      `json:"additionalProperties,omitempty"`
	Defs                 map[string]*Node `json:"$defs,omitempty"`
	Items                *Node            `json:"items,omitempty"`
	MinItems             *int             `json:"minItems,omitempty"`
	MaxItems             *int             `json:"maxItems,omitempty"`
	MinLength            *int             `json:"minLength,omitempty"`
	MaxLength            *int             `json:"maxLength,omitempty"`
	Minimum              *json.Number     `json:"minimum,omitempty"`
	Maximum              *json.Number     `json:"maximum,omitempty"`
	Pattern              string           `json:"pattern,omitempty"`
	Format               string           `json:"format,omitempty"`
	AnyOf                []*Node          `json:"anyOf,omitempty"`
	OneOf                []*Node          `json:"oneOf,omitempty"`
	AllOf                []*Node          `json:"allOf,omitempty"`
	If                   *Node            `json:"if,omitempty"`
	Then                 *Node            `json:"then,omitempty"`
	Else                 *Node            `json:"else,omitempty"`
	Not                  *Node            `json:"not,omitempty"`
	GoType               string           `json:"x-go-type,omitempty"`
	GoPrefix             string           `json:"x-go-prefix,omitempty"`
	pattern              *regexp.Regexp
}

func Parse(data []byte) (*Node, error) {
	var root Node
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&root); err != nil {
		return nil, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("trailing schema data")
	}
	if root.Dialect != "https://json-schema.org/draft/2020-12/schema" {
		return nil, fmt.Errorf("expected draft 2020-12")
	}
	var visit func(*Node) error
	visit = func(n *Node) error {
		if n == nil {
			return nil
		}
		if !slices.Contains([]string{"", "object", "array", "string", "boolean", "number", "integer", "null"}, n.Type) {
			return fmt.Errorf("unsupported type %q", n.Type)
		}
		if n.Ref != "" && (!strings.HasPrefix(n.Ref, "#/$defs/") || root.Defs[strings.TrimPrefix(n.Ref, "#/$defs/")] == nil) {
			return fmt.Errorf("unresolved reference %q", n.Ref)
		}
		if n.Format != "" && n.Format != "date-time" {
			return fmt.Errorf("unsupported format %q", n.Format)
		}
		if n.Pattern != "" {
			pattern, err := regexp.Compile(n.Pattern)
			if err != nil {
				return err
			}
			n.pattern = pattern
		}
		for _, children := range []map[string]*Node{n.Properties, n.Defs} {
			for _, child := range children {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		for _, children := range [][]*Node{n.AnyOf, n.OneOf, n.AllOf} {
			for _, child := range children {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		if n.AdditionalProperties != nil && n.AdditionalProperties.Schema != nil {
			if err := visit(n.AdditionalProperties.Schema); err != nil {
				return err
			}
		}
		for _, child := range []*Node{n.Items, n.If, n.Then, n.Else, n.Not} {
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(&root); err != nil {
		return nil, err
	}
	return &root, nil
}

func (n *Node) Validate(data []byte, root *Node) error {
	var value any
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return n.validate(value, root, "$")
}

func (n *Node) validate(value any, root *Node, path string) error {
	fail := func(rule string) error { return fmt.Errorf("%s: %s", path, rule) }
	if n.Ref != "" {
		if err := root.Defs[strings.TrimPrefix(n.Ref, "#/$defs/")].validate(value, root, path); err != nil {
			return err
		}
	}
	if len(n.Const) != 0 {
		var expected any
		d := json.NewDecoder(bytes.NewReader(n.Const))
		d.UseNumber()
		_ = d.Decode(&expected)
		// The protocol only uses scalar constants.
		if !reflect.DeepEqual(value, expected) {
			return fail("unexpected constant")
		}
	}
	if len(n.Enum) != 0 {
		text, ok := value.(string)
		if !ok || !slices.Contains(n.Enum, text) {
			return fail("unknown enum value")
		}
	}
	for _, alternatives := range []struct {
		nodes      []*Node
		exactlyOne bool
	}{{n.AnyOf, false}, {n.OneOf, true}} {
		if len(alternatives.nodes) == 0 {
			continue
		}
		matches := 0
		for _, child := range alternatives.nodes {
			if child.validate(value, root, path) == nil {
				matches++
			}
		}
		if matches == 0 || (alternatives.exactlyOne && matches != 1) {
			return fail("invalid union variant")
		}
	}
	for _, child := range n.AllOf {
		if err := child.validate(value, root, path); err != nil {
			return err
		}
	}
	if n.Not != nil && n.Not.validate(value, root, path) == nil {
		return fail("forbidden value")
	}
	if n.If != nil {
		child := n.Else
		if n.If.validate(value, root, path) == nil {
			child = n.Then
		}
		if child != nil {
			if err := child.validate(value, root, path); err != nil {
				return err
			}
		}
	}
	switch n.Type {
	case "null":
		if value != nil {
			return fail("expected null")
		}
	case "object":
		if _, ok := value.(map[string]any); !ok {
			return fail("expected object")
		}
	case "array":
		if _, ok := value.([]any); !ok {
			return fail("expected array")
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fail("expected string")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fail("expected boolean")
		}
	case "number", "integer":
		number, ok := value.(json.Number)
		if !ok {
			return fail("expected number")
		}
		rational, ok := new(big.Rat).SetString(string(number))
		if !ok || (n.Type == "integer" && !rational.IsInt()) {
			return fail("expected integer")
		}
	case "":
	default:
		return fail("unsupported schema type")
	}
	if object, ok := value.(map[string]any); ok {
		for _, key := range n.Required {
			if _, found := object[key]; !found {
				return fail("missing field " + key)
			}
		}
		for key, v := range object {
			if child, found := n.Properties[key]; found {
				if err := child.validate(v, root, path+"."+key); err != nil {
					return err
				}
			} else if n.AdditionalProperties != nil {
				if n.AdditionalProperties.Schema != nil {
					if err := n.AdditionalProperties.Schema.validate(v, root, path+"."+key); err != nil {
						return err
					}
				} else if !n.AdditionalProperties.Allowed {
					return fail("unknown field " + key)
				}
			}
		}
	}
	if array, ok := value.([]any); ok {
		if n.MinItems != nil && len(array) < *n.MinItems || n.MaxItems != nil && len(array) > *n.MaxItems {
			return fail("array size outside bounds")
		}
		if n.Items != nil {
			for i, item := range array {
				if err := n.Items.validate(item, root, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	if text, ok := value.(string); ok {
		length := len([]rune(text))
		if n.MinLength != nil && length < *n.MinLength || n.MaxLength != nil && length > *n.MaxLength {
			return fail("string size outside bounds")
		}
		if n.pattern != nil && !n.pattern.MatchString(text) {
			return fail("string pattern mismatch")
		}
		if n.Format == "date-time" {
			if _, err := time.Parse(time.RFC3339Nano, text); err != nil {
				return fail("invalid date-time")
			}
		}
	}
	if number, ok := value.(json.Number); ok && (n.Minimum != nil || n.Maximum != nil) {
		value, ok := new(big.Rat).SetString(string(number))
		if !ok {
			return fail("invalid number")
		}
		if n.Minimum != nil {
			minimum, ok := new(big.Rat).SetString(string(*n.Minimum))
			if !ok || value.Cmp(minimum) < 0 {
				return fail("number below minimum")
			}
		}
		if n.Maximum != nil {
			maximum, ok := new(big.Rat).SetString(string(*n.Maximum))
			if !ok || value.Cmp(maximum) > 0 {
				return fail("number above maximum")
			}
		}
	}
	return nil
}
