// SPDX-License-Identifier: Apache-2.0
package jobapi

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
)

// PlanInput is Core's immutable snapshot of an accepted record, never authority
// to create children. ResultID and BaseRevision fence replacement of a preview.
type PlanInput struct {
	RootID       string `json:"root_id"`
	RootRevision string `json:"root_revision"`
	Kind         string `json:"kind"`
	Title        string `json:"title"`
	Done         string `json:"done"`
	ResultID     string `json:"result_id"`
	BaseRevision int    `json:"base_revision"`
	Instruction  string `json:"instruction"`
	TurnID       string `json:"turn_id"`
}
type PlanDraft struct {
	Title   string      `json:"title"`
	Summary string      `json:"summary"`
	Entries []PlanEntry `json:"entries"`
}
type PlanEntry struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Parent    string   `json:"parent"`
	Done      string   `json:"done"`
	Stage     string   `json:"stage,omitempty"`
	DependsOn []string `json:"depends_on,omitempty"`
}

func DecodePlan(raw []byte, kind string) (PlanDraft, error) {
	var d PlanDraft
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&d) != nil || dec.Decode(new(any)) != io.EOF {
		return d, ErrInvalid
	}
	return d, ValidatePlan(kind, d)
}
func ValidatePlan(kind string, d PlanDraft) error {
	if (kind != "goal" && kind != "project") || strings.TrimSpace(d.Title) == "" || len([]rune(d.Title)) > 200 || strings.TrimSpace(d.Summary) == "" || len([]rune(d.Summary)) > 600 || len(d.Entries) == 0 || len(d.Entries) > 999 {
		return ErrInvalid
	}
	entries := map[string]PlanEntry{}
	for _, e := range d.Entries {
		if e.ID == "" || len(e.ID) > 200 || strings.TrimSpace(e.Title) == "" || len([]rune(e.Title)) > 200 || strings.TrimSpace(e.Done) == "" || len(e.Done) > 4096 || len(e.Stage) > 200 || strings.ContainsAny(e.Title+e.Stage, "\r\n") {
			return ErrInvalid
		}
		if _, ok := entries[e.ID]; ok {
			return ErrInvalid
		}
		switch e.Kind {
		case "project":
			if kind != "goal" || e.Parent != "" || e.Stage != "" {
				return ErrInvalid
			}
		case "todo":
			if kind == "goal" {
				if entries[e.Parent].Kind != "project" {
					return ErrInvalid
				}
			} else if e.Parent != "" {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		entries[e.ID] = e
	}
	visiting, done := map[string]bool{}, map[string]bool{}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		e, ok := entries[id]
		if !ok {
			return false
		}
		visiting[id] = true
		seen := map[string]bool{}
		for _, dep := range e.DependsOn {
			if seen[dep] || !visit(dep) {
				return false
			}
			seen[dep] = true
		}
		visiting[id] = false
		done[id] = true
		return true
	}
	for id := range entries {
		if !visit(id) {
			return ErrInvalid
		}
	}
	return nil
}
