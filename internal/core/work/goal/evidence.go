// SPDX-License-Identifier: Apache-2.0
package goal

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// CollectedEvidence is produced by a trusted Core adapter, never by a worker's
// completion receipt. The ledger retains the output and the actual inputs to
// citation/rubric checks, rather than only a location or a successful exit code.
type CollectedEvidence struct {
	Record   json.RawMessage `json:"record,omitempty"`
	Output   string          `json:"output,omitempty"`
	Reads    []EvidenceRead  `json:"reads,omitempty"`
	Rubric   string          `json:"rubric,omitempty"`
	Judgment string          `json:"judgment,omitempty"`
}

type EvidenceRead struct {
	Target   string `json:"target"`
	Revision string `json:"revision"`
	Content  string `json:"content"`
}

func recordObject(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return json.Unmarshal(raw, &fields) == nil && len(fields) > 0
}

func (e *CollectedEvidence) Valid(c CriterionCheck) bool {
	if e == nil {
		return false
	}
	reads := len(e.Reads) > 0
	for _, r := range e.Reads {
		reads = reads && strings.TrimSpace(r.Target) != "" && strings.TrimSpace(r.Revision) != "" && strings.TrimSpace(r.Content) != ""
	}
	switch c.Kind {
	case ReadEvidence:
		return recordObject(e.Record)
	case RunEvidence:
		return strings.TrimSpace(e.Output) != ""
	case CitationEvidence:
		return reads && strings.TrimSpace(e.Output) != ""
	case JudgmentEvidence:
		return reads && strings.TrimSpace(c.Rubric) != "" && e.Rubric == c.Rubric && strings.TrimSpace(e.Judgment) != ""
	}
	return false
}

// Matches compares the requested fields with the observed record. Extra fields
// from the external system are allowed; arrays and scalar types must match.
func (e *CollectedEvidence) Matches(c CriterionCheck) bool {
	if !e.Valid(c) {
		return false
	}
	if len(c.Expected) == 0 {
		return true
	}
	if c.Kind != ReadEvidence || !recordObject(c.Expected) {
		return false
	}
	decode := func(raw []byte) any {
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var v any
		_ = d.Decode(&v)
		return v
	}
	return containsRecord(decode(e.Record), decode(c.Expected))
}

func containsRecord(actual, expected any) bool {
	fields, ok := expected.(map[string]any)
	if !ok {
		return reflect.DeepEqual(actual, expected)
	}
	got, ok := actual.(map[string]any)
	if !ok {
		return false
	}
	for key, value := range fields {
		read, exists := got[key]
		if !exists || !containsRecord(read, value) {
			return false
		}
	}
	return true
}
