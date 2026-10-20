// SPDX-License-Identifier: Apache-2.0

// Package conformance loads the language-neutral client fixture corpus and
// checks codecs and receive boundaries. It does not simulate application state.
package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"reflect"
	"slices"
	"strings"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Case describes one fresh receive boundary. Output names the admitted
// envelopes in order; Errors records one refusal code (or empty string) per
// input. Outcome is read from the implementation, not inferred by the runner.
type Case struct {
	Name    string               `json:"-"`
	Side    string               `json:"side"`
	Input   []string             `json:"input"`
	Output  []string             `json:"output"`
	Errors  []protocol.ErrorCode `json:"errors"`
	Outcome protocol.DecodeStats `json:"outcome"`
}

type Suite struct {
	Envelopes map[string]json.RawMessage
	Cases     []Case
	inputs    map[string]json.RawMessage
}

// Load reads every JSON file below fsys. cases/ contains case descriptions,
// invalid/ contains schema-invalid input, and all other JSON files are valid
// envelopes that every codec must round-trip. No files are rewritten.
func Load(fsys fs.FS) (*Suite, error) {
	s := &Suite{Envelopes: map[string]json.RawMessage{}, inputs: map[string]json.RawMessage{}}
	err := fs.WalkDir(fsys, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return nil
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("%s: invalid JSON", name)
		}
		if strings.HasPrefix(name, "cases/") {
			var c Case
			d := json.NewDecoder(bytes.NewReader(data))
			d.DisallowUnknownFields()
			if err := d.Decode(&c); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			c.Name = name
			s.Cases = append(s.Cases, c)
		} else {
			s.inputs[name] = data
			if !strings.HasPrefix(name, "invalid/") {
				s.Envelopes[name] = data
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(s.Envelopes) == 0 || len(s.Cases) == 0 {
		return nil, fmt.Errorf("fixture suite needs envelopes and cases")
	}
	used := map[string]bool{}
	sides := map[string]bool{}
	for _, c := range s.Cases {
		if c.Side != "server" && c.Side != "client" {
			return nil, fmt.Errorf("%s: unknown side %q", c.Name, c.Side)
		}
		sides[c.Side] = true
		if len(c.Input) == 0 || c.Output == nil || len(c.Errors) != len(c.Input) {
			return nil, fmt.Errorf("%s: input, output and per-input errors are required", c.Name)
		}
		var malformed uint64
		for _, code := range c.Errors {
			if code == protocol.ErrorMalformed {
				malformed++
			} else if code != "" {
				return nil, fmt.Errorf("%s: unsupported decoder error %q", c.Name, code)
			}
		}
		if c.Outcome.Accepted != uint64(len(c.Output)) || c.Outcome.Malformed != malformed ||
			c.Outcome.Accepted+c.Outcome.Malformed+c.Outcome.UnknownKinds != uint64(len(c.Input)) ||
			(c.Side == "server" && c.Outcome.UnknownKinds != 0) {
			return nil, fmt.Errorf("%s: inconsistent expected counts", c.Name)
		}
		for _, name := range c.Input {
			if !fs.ValidPath(name) || s.inputs[name] == nil {
				return nil, fmt.Errorf("%s: missing input %q", c.Name, name)
			}
			used[name] = true
		}
		for _, name := range c.Output {
			if s.Envelopes[name] == nil {
				return nil, fmt.Errorf("%s: output %q is not a valid envelope fixture", c.Name, name)
			}
		}
	}
	if !sides["client"] || !sides["server"] {
		return nil, fmt.Errorf("fixture suite needs both receiving sides")
	}
	for name := range s.inputs {
		if !used[name] {
			return nil, fmt.Errorf("%s: envelope is not exercised by a case", name)
		}
	}
	return s, nil
}

// RoundTrip checks every positive envelope against the implementation's codec.
// JSON object order and whitespace do not matter. UseNumber avoids rounding
// large integers or decimals through float64 during comparison.
func (s *Suite) RoundTrip(codec func([]byte) ([]byte, error)) error {
	names := make([]string, 0, len(s.Envelopes))
	for name := range s.Envelopes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		input := s.Envelopes[name]
		output, err := codec(append([]byte(nil), input...))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if err := equalJSON(input, output); err != nil {
			return fmt.Errorf("%s: round trip: %w", name, err)
		}
	}
	return nil
}

// Receiver adapts an implementation to the runner. Decode returns an admitted
// envelope, a typed refusal, or neither for an ignored frame. Its error return
// is reserved for harness failures. Stats returns the receiver's own counters.
type Receiver struct {
	Decode func([]byte) (json.RawMessage, protocol.ErrorCode, error)
	Stats  func() protocol.DecodeStats
}

func (s *Suite) Run(c Case, receiver Receiver) error {
	if receiver.Decode == nil || receiver.Stats == nil {
		return fmt.Errorf("%s: missing receiver", c.Name)
	}
	if receiver.Stats() != (protocol.DecodeStats{}) {
		return fmt.Errorf("%s: receiver must be fresh", c.Name)
	}
	outputs := 0
	for i, name := range c.Input {
		data, code, err := receiver.Decode(append([]byte(nil), s.inputs[name]...))
		if err != nil {
			return fmt.Errorf("%s input %d: %w", c.Name, i, err)
		}
		if code != c.Errors[i] {
			return fmt.Errorf("%s input %d: error code %q, want %q", c.Name, i, code, c.Errors[i])
		}
		if len(data) != 0 {
			if code != "" || outputs >= len(c.Output) {
				return fmt.Errorf("%s input %d: unexpected output", c.Name, i)
			}
			if err := equalJSON(s.Envelopes[c.Output[outputs]], data); err != nil {
				return fmt.Errorf("%s output %d: %w", c.Name, outputs, err)
			}
			outputs++
		}
	}
	if outputs != len(c.Output) {
		return fmt.Errorf("%s: outputs %d, want %d", c.Name, outputs, len(c.Output))
	}
	if stats := receiver.Stats(); stats != c.Outcome {
		return fmt.Errorf("%s: recorded %+v, want %+v", c.Name, stats, c.Outcome)
	}
	return nil
}

func equalJSON(a, b []byte) error {
	decode := func(data []byte) (any, error) {
		var value any
		d := json.NewDecoder(bytes.NewReader(data))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return nil, err
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return nil, fmt.Errorf("trailing JSON data")
		}
		return value, nil
	}
	x, err := decode(a)
	if err != nil {
		return err
	}
	y, err := decode(b)
	if err != nil {
		return err
	}
	if !equalValue(x, y) {
		return fmt.Errorf("JSON value changed")
	}
	return nil
}

func equalValue(a, b any) bool {
	switch a := a.(type) {
	case json.Number:
		b, ok := b.(json.Number)
		if !ok {
			return false
		}
		x, xOK := new(big.Rat).SetString(string(a))
		y, yOK := new(big.Rat).SetString(string(b))
		return xOK && yOK && x.Cmp(y) == 0
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for key, value := range a {
			other, present := b[key]
			if !present || !equalValue(value, other) {
				return false
			}
		}
		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}
		for i, value := range a {
			if !equalValue(value, b[i]) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a, b)
	}
}
