// SPDX-License-Identifier: Apache-2.0

package pin

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// Input deliberately excludes the expected judgment. Only the runner can read
// the recorded errors; the implementation under test receives inputs alone.
type Input struct {
	Fixture  string
	Index    int
	Schema   string
	Label    string
	Instance json.RawMessage
}

type Result struct{ Cases int }

type judgment struct {
	Schema   string   `json:"schema"`
	Label    string   `json:"label"`
	Instance string   `json:"instance"`
	Errors   []string `json:"errors"`
}

// Run verifies the full pin and parses every adopted case before executing any
// of them. A returned execution error is never treated as a negative verdict.
// Callers supply LINA's record validator, not a CRW executable or replay output.
func Run(r Record, corpus fs.FS, execute func(Input) (bool, error)) (Result, error) {
	var result Result
	if err := r.Verify(corpus); err != nil {
		return result, err
	}
	if execute == nil {
		return result, fmt.Errorf("CRW runner requires an executor")
	}
	schemas := make(map[string]string)
	for _, f := range r.Schemas {
		schemas[strings.TrimSuffix(path.Base(f.Path), ".json")] = f.SHA256
	}
	type scenario struct {
		input Input
		valid bool
	}
	var cases []scenario
	for _, f := range r.Fixtures {
		data, err := fs.ReadFile(corpus, f.Path)
		if err != nil {
			return result, err
		}
		if strings.HasSuffix(f.Path, ".gz") {
			reader, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return result, fmt.Errorf("%s: %w", f.Path, err)
			}
			data, err = io.ReadAll(io.LimitReader(reader, 8<<20+1))
			closeErr := reader.Close()
			if err != nil || closeErr != nil {
				return result, fmt.Errorf("%s: %w", f.Path, errors.Join(err, closeErr))
			}
			if len(data) > 8<<20 {
				return result, fmt.Errorf("%s: fixture exceeds 8 MiB", f.Path)
			}
		}
		data = bytes.TrimSpace(data)
		var verdicts []judgment
		var schemaHash string
		if len(data) > 0 && data[0] == '[' {
			err = json.Unmarshal(data, &verdicts)
		} else {
			var envelope struct {
				SchemaHash string     `json:"schemaSha256"`
				Verdicts   []judgment `json:"verdicts"`
			}
			err = json.Unmarshal(data, &envelope)
			verdicts, schemaHash = envelope.Verdicts, envelope.SchemaHash
			if err == nil && !digest.MatchString(schemaHash) {
				err = fmt.Errorf("supplemental fixture requires schemaSha256")
			}
		}
		if err != nil {
			return result, fmt.Errorf("%s: %w", f.Path, err)
		}
		if len(verdicts) == 0 {
			return result, fmt.Errorf("%s: fixture contains no judgments", f.Path)
		}
		for i, v := range verdicts {
			sha, known := schemas[v.Schema]
			if !known || v.Label == "" || !json.Valid([]byte(v.Instance)) || v.Errors == nil || schemaHash != "" && schemaHash != sha {
				return result, fmt.Errorf("%s case %d (%s): incomplete judgment, unknown schema or schema hash mismatch", f.Path, i, v.Label)
			}
			cases = append(cases, scenario{Input{f.Path, i, v.Schema, v.Label, json.RawMessage(v.Instance)}, len(v.Errors) == 0})
		}
	}
	inputs := make([]Input, len(cases))
	for i, c := range cases {
		inputs[i] = c.input
	}
	inputs, err := replayInputs(r, corpus, inputs)
	if err != nil {
		return result, err
	}
	var failures []error
	for i, c := range cases {
		c.input = inputs[i]
		valid, err := execute(c.input)
		result.Cases++
		if err != nil {
			failures = append(failures, fmt.Errorf("%s case %d (%s): execute: %w", c.input.Fixture, c.input.Index, c.input.Label, err))
		} else if valid != c.valid {
			failures = append(failures, fmt.Errorf("%s case %d (%s): valid=%t, fixture expects %t", c.input.Fixture, c.input.Index, c.input.Label, valid, c.valid))
		}
	}
	return result, errors.Join(failures...)
}
