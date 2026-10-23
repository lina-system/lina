// SPDX-License-Identifier: Apache-2.0
package persona

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxWorkMethodBytes = 16 << 10

var ErrWorkMethod = errors.New("invalid_work_method")

type methodField struct {
	fallback string
	choices  []string
	kind     string
}

func choice(fallback string, choices ...string) methodField {
	return methodField{fallback, choices, "string"}
}
func list(fallback string, choices ...string) methodField {
	return methodField{fallback, choices, "list"}
}

// The schema and defaults are Core data, shared by publication and fill validation.
var methodFields = map[string]methodField{
	"tools.declare_first": list(`[]`, "work", "github", "terminal", "device", "setup", "to_do", "mail", "calendar", "messaging_read", "documents", "people_records", "wiki", "life_records", "web_research", "creative_artifacts", "image_generation", "materials"),
	"workers.dispatch":    {fallback: `false`, kind: "bool"},
	"background_runs":     list(`["research","reading","drafting","checks"]`, "research", "reading", "drafting", "checks"),
	"reply.status":        choice(`"verdict-first"`, "verdict-first"),
	"reply.comparison":    choice(`"verdict-then-card"`, "verdict-then-card", "verdict-then-table"),
	"reply.candidates":    {fallback: `1`, kind: "positive"},
	"reply.third_party":   choice(`"recipient-only"`, "recipient-only"),
	"ask.per_turn":        {fallback: `1`, kind: "positive"},
	"ask.form":            choice(`"narrowed"`, "narrowed", "choices"),
	"ask.unanswered":      choice(`"default"`, "default"),
	"handoff.owns":        list(`[]`, "setup", "device", "account", "health_of_lina", "schedule", "to_do", "mail", "business_document", "coordination", "code", "pull_request", "worker_task", "people_record", "wiki", "life_record", "inventory", "diet_log", "creative_artifact", "creative_tooling_brief", "investment_analysis", "contract_draft", "tax_calculation", "legal_review"),
	"handoff.check_back":  {fallback: `true`, kind: "bool"},
}
var doneEvidence = map[string]string{"service": "read_back_running", "interface": "real_view", "data": "live_vs_shown", "message": "sent_copy", "code": "owner_path_run", "document": "source_reopened", "purchase": "receipt", "peer_report": "labelled_claim"}

func init() {
	var kinds []string
	for _, kind := range doneEvidence {
		kinds = append(kinds, kind)
	}
	for work, kind := range doneEvidence {
		methodFields["done."+work] = list(`["`+kind+`"]`, kinds...)
	}
}

// Reject duplicate object keys, nulls and trailing JSON as well as bad syntax.
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		if token == nil {
			return ErrWorkMethod
		}
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return ErrWorkMethod
			}
			seen[key] = true
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return ErrWorkMethod
	}
	_, err = d.Token()
	return err
}

func methodObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, ErrWorkMethod
	}
	return object, nil
}

func validMethodValue(key string, raw json.RawMessage) bool {
	spec, ok := methodFields[key]
	if !ok {
		return false
	}
	switch spec.kind {
	case "string":
		var v string
		return json.Unmarshal(raw, &v) == nil && slices.Contains(spec.choices, v)
	case "bool":
		var v bool
		return string(raw) != "null" && json.Unmarshal(raw, &v) == nil
	case "positive":
		var v int
		return json.Unmarshal(raw, &v) == nil && v > 0
	case "list":
		var values []string
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return false
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !slices.Contains(spec.choices, v) || seen[v] {
				return false
			}
			seen[v] = true
		}
		if work, ok := strings.CutPrefix(key, "done."); ok && !seen[doneEvidence[work]] {
			return false
		}
		return true
	}
	return false
}

// EmptyWorkMethod is also used for agents created from a description (key="").
func EmptyWorkMethod(key string) string {
	if key == "" {
		return `{"schema":1}`
	}
	raw, _ := json.Marshal(struct {
		Schema int    `json:"schema"`
		Preset string `json:"preset"`
	}{1, key})
	return string(raw)
}

// ParseWorkMethod returns canonical JSON, so formatting alone mints no revision.
// A nonempty key must be a known starter and match the record's preset exactly.
func ParseWorkMethod(data []byte, key string) (string, error) {
	if len(data) > MaxWorkMethodBytes || !utf8.Valid(data) {
		return "", ErrWorkMethod
	}
	d := json.NewDecoder(bytes.NewReader(data))
	if uniqueJSON(d) != nil {
		return "", ErrWorkMethod
	}
	if _, err := d.Token(); err != io.EOF {
		return "", ErrWorkMethod
	}
	object, err := methodObject(bytes.TrimSpace(data))
	if err != nil {
		return "", err
	}
	var schema int
	if json.Unmarshal(object["schema"], &schema) != nil || schema != 1 {
		return "", ErrWorkMethod
	}
	var preset string
	if raw, ok := object["preset"]; ok {
		if json.Unmarshal(raw, &preset) != nil || !IsStarter(preset) {
			return "", ErrWorkMethod
		}
	}
	if preset != key || key != "" && !IsStarter(key) {
		return "", ErrWorkMethod
	}
	for name, raw := range object {
		switch name {
		case "schema", "preset":
		case "fill":
			var keys []string
			if json.Unmarshal(raw, &keys) != nil || keys == nil {
				return "", ErrWorkMethod
			}
			seen := map[string]bool{}
			for _, k := range keys {
				if _, ok := methodFields[k]; !ok || seen[k] {
					return "", ErrWorkMethod
				}
				seen[k] = true
			}
		case "background_runs":
			if !validMethodValue(name, raw) {
				return "", ErrWorkMethod
			}
		case "tools", "workers", "reply", "done", "ask", "handoff":
			fields, err := methodObject(bytes.TrimSpace(raw))
			if err != nil {
				return "", err
			}
			for field, value := range fields {
				if !validMethodValue(name+"."+field, value) {
					return "", ErrWorkMethod
				}
			}
		default:
			return "", ErrWorkMethod
		}
	}
	values := WorkMethodValues(string(data))
	if string(values["workers.dispatch"]) == "true" && key != "dev" {
		return "", ErrWorkMethod
	}
	// Marshal through values to canonicalize nested objects as well as the root.
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", ErrWorkMethod
	}
	raw, err := json.Marshal(value)
	return string(raw), err
}

// WorkMethodValues returns a new flat, typed data projection including defaults.
// Callers supply a validated record; the projection never carries persona text.
func WorkMethodValues(record string) map[string]json.RawMessage {
	values := make(map[string]json.RawMessage, len(methodFields))
	for key, field := range methodFields {
		values[key] = json.RawMessage(field.fallback)
	}
	var object map[string]json.RawMessage
	_ = json.Unmarshal([]byte(record), &object)
	for key, raw := range object {
		if key == "background_runs" {
			values[key] = raw
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil {
			for name, value := range fields {
				values[key+"."+name] = value
			}
		}
	}
	return values
}

func WorkMethodFillKeys(record string) []string {
	var object struct {
		Fill []string `json:"fill"`
	}
	_ = json.Unmarshal([]byte(record), &object)
	return object.Fill
}

// FillWorkMethod checks both declaration and schema, including cross-field rules.
func FillWorkMethod(record, key string, value json.RawMessage) (string, error) {
	if !slices.Contains(WorkMethodFillKeys(record), key) || !validMethodValue(key, value) {
		return "", ErrWorkMethod
	}
	object, err := methodObject([]byte(record))
	if err != nil {
		return "", err
	}
	group, field, nested := strings.Cut(key, ".")
	if nested {
		fields := map[string]json.RawMessage{}
		if raw, ok := object[group]; ok {
			if json.Unmarshal(raw, &fields) != nil {
				return "", ErrWorkMethod
			}
		}
		fields[field] = value
		object[group], _ = json.Marshal(fields)
	} else {
		object[key] = value
	}
	var preset string
	_ = json.Unmarshal(object["preset"], &preset)
	raw, err := json.Marshal(object)
	if err != nil {
		return "", ErrWorkMethod
	}
	return ParseWorkMethod(raw, preset)
}

func WorkMethodRevision(record string) string {
	sum := sha256.Sum256([]byte(record))
	return hex.EncodeToString(sum[:])
}

// LoadWorkMethod distinguishes a missing record (usable empty fallback) from
// invalid data (refuse the entire preset publication). Source is confined by Core.
func LoadWorkMethod(source fs.FS, key string) (record string, missing bool, err error) {
	if source == nil || !IsStarter(key) {
		return "", false, ErrWorkMethod
	}
	file, err := source.Open(key + "/work-method-v1.json")
	if errors.Is(err, fs.ErrNotExist) {
		return EmptyWorkMethod(key), true, nil
	}
	if err != nil {
		return "", false, ErrWorkMethod
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxWorkMethodBytes {
		return "", false, ErrWorkMethod
	}
	raw, err := io.ReadAll(io.LimitReader(file, MaxWorkMethodBytes+1))
	if err != nil {
		return "", false, ErrWorkMethod
	}
	record, err = ParseWorkMethod(raw, key)
	return record, false, err
}

func WorkMethodWarning(key string, missing bool) string {
	if !IsStarter(key) {
		key = "unknown"
	}
	if missing {
		return "Shipped work method " + key + ": missing; using defaults."
	}
	return "Shipped work method " + key + ": invalid; keeping the previous revision."
}
