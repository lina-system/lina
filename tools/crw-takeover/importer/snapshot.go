// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Ref belongs only to the operator export and report, never a product index.
type Ref struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
}

func (r Ref) key() string { b, _ := json.Marshal(r); return string(b) }

type sourceRecord struct {
	Ref
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Done          bool            `json:"done"`
	DoneCondition string          `json:"done_condition"`
	Deadline      string          `json:"deadline"`
	Parent        *Ref            `json:"parent"`
	Project       *Ref            `json:"project"`
	Intent        string          `json:"intent"`
	Payload       json.RawMessage `json:"payload"`
}
type snapshot struct {
	Version int               `json:"version"`
	Counts  map[string]int    `json:"counts"`
	Records []json.RawMessage `json:"records"`
}
type Row struct {
	Index int `json:"index"`
	Ref
	Outcome string `json:"outcome"`
	LINAID  string `json:"lina_id,omitempty"`
	Reused  bool   `json:"reused,omitempty"`
	Reason  string `json:"reason,omitempty"`
}
type Report struct {
	Version int    `json:"version"`
	SHA256  string `json:"sha256"`
	Rows    []Row  `json:"records"`
}

// Validate requires one outcome for every input position, including malformed
// records without usable identities. Summary counts alone are not evidence.
func (r Report) Validate(count int) error {
	if len(r.Rows) != count {
		return errors.New("report does not cover every fetched record")
	}
	seen, ids := map[int]bool{}, map[string]bool{}
	for _, row := range r.Rows {
		if row.Index < 0 || row.Index >= count || seen[row.Index] {
			return errors.New("duplicate or invalid report position")
		}
		seen[row.Index] = true
		switch row.Outcome {
		case "imported":
			if row.LINAID == "" || ids[row.LINAID] {
				return errors.New("imported record lacks a unique LINA id")
			}
			ids[row.LINAID] = true
		case "lost", "unsupported":
			if row.Reason == "" {
				return errors.New("unimported record lacks a reason")
			}
		default:
			return errors.New("invalid report outcome")
		}
	}
	return nil
}
func decode(raw []byte, into any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing snapshot data")
	}
	return nil
}

// Verify checks the envelope and digest before opening any destination writer.
func Verify(data []byte, hash string) error { _, err := verified(data, hash); return err }

func verified(data []byte, hash string) (snapshot, error) {
	var s snapshot
	if len(data) > 64<<20 || len(hash) != 64 || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		return s, errors.New("snapshot SHA-256 mismatch or size limit")
	}
	if err := decode(data, &s); err != nil {
		return s, err
	}
	if s.Version != 1 || s.Records == nil || s.Counts == nil {
		return s, errors.New("invalid snapshot envelope")
	}
	counts := map[string]int{}
	for _, raw := range s.Records {
		var r struct{ Source string }
		_ = json.Unmarshal(raw, &r)
		counts[r.Source]++
	}
	for source, n := range s.Counts {
		if n < 0 || counts[source] != n {
			return s, errors.New("snapshot inventory mismatch")
		}
		delete(counts, source)
	}
	if len(counts) != 0 {
		return s, errors.New("snapshot inventory omits fetched records")
	}
	return s, nil
}
func classify(raw json.RawMessage) (sourceRecord, string, string) {
	var r sourceRecord
	// Decode known fields but retain the entire original JSON as observation data.
	if len(raw) > 1<<20 || json.Unmarshal(raw, &r) != nil || r.Source == "" || r.Kind == "" || r.ID == "" {
		return r, "lost", "invalid source record or identity"
	}
	kind := ""
	switch r.Source {
	case "tracker":
		switch r.Kind {
		case "initiative":
			kind = "goal"
		case "project":
			kind = "project"
		case "issue", "milestone":
			kind = "todo"
		case "plan", "completion_criteria", "decision", "scope", "parent_binding":
			kind = "document"
		}
	case "intent":
		if r.Kind == "intent_card" {
			kind = "intent_card"
		}
	case "crw":
		switch r.Kind {
		case "relay_store", "scope", "assignment", "relationship", "link", "parent_binding", "attempt", "receipt", "verdict", "acknowledgement", "branch", "worktree", "completion_criteria", "decision":
			kind = "document"
		}
	}
	if kind == "" {
		return r, "unsupported", "source kind has no LINA mapping"
	}
	if strings.TrimSpace(r.Title) == "" || strings.ContainsAny(r.Title, "\r\n") || len(r.Title) > 512 {
		return r, "lost", "missing or invalid title"
	}
	if r.Kind == "project" && strings.TrimSpace(r.DoneCondition) == "" {
		return r, "lost", "project lacks a done condition"
	}
	if r.Kind == "milestone" {
		if _, err := time.Parse("2006-01-02", r.Deadline); err != nil || strings.TrimSpace(r.DoneCondition) == "" {
			return r, "lost", "milestone needs a day deadline and done condition"
		}
	}
	if r.Source == "crw" && (len(r.Payload) == 0 || string(r.Payload) == "null") {
		return r, "lost", "CRW observation lacks its payload"
	}
	return r, kind, ""
}
