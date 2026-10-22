// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"reflect"
	"regexp"
	"strings"
)

type replayInput struct {
	Fixture          string          `json:"fixture"`
	Index            int             `json:"index"`
	Schema           string          `json:"schema"`
	Label            string          `json:"label"`
	NormalizedSHA256 string          `json:"normalizedSHA256"`
	Instance         json.RawMessage `json:"instance"`
}

// replayInputs cannot see expected judgments. It substitutes only a pinned
// input with the same fixture identity and normalized shape.
func replayInputs(r Record, corpus fs.FS, inputs []Input) ([]Input, error) {
	if r.Replay == nil {
		return inputs, nil
	}
	raw, err := fs.ReadFile(corpus, r.Replay.Path)
	if err != nil {
		return nil, err
	}
	var document struct {
		Version int               `json:"version"`
		Sources map[string]string `json:"sources"`
		Inputs  []replayInput     `json:"inputs"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("replay inputs: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("trailing replay input data")
	}
	if document.Version != 1 || len(document.Sources) == 0 || len(document.Inputs) != len(inputs) {
		return nil, fmt.Errorf("incomplete replay input coverage")
	}
	for source, hash := range document.Sources {
		if !validPath(source) || !digest.MatchString(hash) {
			return nil, fmt.Errorf("invalid replay input source %q", source)
		}
	}
	type key struct {
		file  string
		index int
	}
	byKey := make(map[key]replayInput)
	for _, item := range document.Inputs {
		k := key{item.Fixture, item.Index}
		if _, duplicate := byKey[k]; duplicate {
			return nil, fmt.Errorf("duplicate replay input %s case %d", k.file, k.index)
		}
		byKey[k] = item
	}
	out := make([]Input, len(inputs))
	for i, input := range inputs {
		item, ok := byKey[key{input.Fixture, input.Index}]
		if !ok || item.Schema != input.Schema || item.Label != input.Label || item.NormalizedSHA256 != fmt.Sprintf("%x", sha256.Sum256(input.Instance)) {
			return nil, fmt.Errorf("replay input %s case %d does not match its source", input.Fixture, input.Index)
		}
		var normalized, materialized any
		if err := json.Unmarshal(input.Instance, &normalized); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(item.Instance, &materialized); err != nil {
			return nil, err
		}
		if !matchesInput(normalized, materialized) {
			return nil, fmt.Errorf("replay input %s case %d changes unmasked data", input.Fixture, input.Index)
		}
		out[i] = input
		out[i].Instance = item.Instance
	}
	return out, nil
}

var placeholder = regexp.MustCompile(`<TMP>|<noise[0-9]+>`)

// The table may restore erased values but cannot change a literal, type, key,
// array member or invalid enum the upstream case actually retains.
func matchesInput(normalized, materialized any) bool {
	switch n := normalized.(type) {
	case map[string]any:
		m, ok := materialized.(map[string]any)
		if !ok || len(n) != len(m) {
			return false
		}
		for k, v := range n {
			mv, ok := m[k]
			if !ok || !matchesInput(v, mv) {
				return false
			}
		}
		return true
	case []any:
		m, ok := materialized.([]any)
		if !ok || len(n) != len(m) {
			return false
		}
		for i, v := range n {
			if !matchesInput(v, m[i]) {
				return false
			}
		}
		return true
	case string:
		m, ok := materialized.(string)
		if !ok || placeholder.MatchString(m) {
			return false
		}
		parts := placeholder.FindAllStringIndex(n, -1)
		if len(parts) == 0 {
			return n == m
		}
		var pattern strings.Builder
		pattern.WriteString("^")
		last := 0
		for _, p := range parts {
			pattern.WriteString(regexp.QuoteMeta(n[last:p[0]]))
			pattern.WriteString(".+")
			last = p[1]
		}
		pattern.WriteString(regexp.QuoteMeta(n[last:]))
		pattern.WriteString("$")
		return regexp.MustCompile(pattern.String()).MatchString(m)
	default:
		return reflect.DeepEqual(normalized, materialized)
	}
}
