// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrAmbiguousDate   = errors.New("ambiguous_date")
	ErrDependencyCycle = errors.New("dependency_cycle")
)

type ChecklistItem struct {
	Text    string `json:"text"`
	Checked bool   `json:"checked"`
}
type Assignee struct {
	Kind string `json:"kind"` // person, agent or worker
	ID   string `json:"id"`
}
type WaitingOn struct {
	Who       string `json:"who"`
	Since     string `json:"since"`
	NextCheck string `json:"next_check"`
}

// Repeat retains the person's rule and its current occurrence. Advancing the
// occurrence belongs to the recurrence engine, never to a view or a model guess.
type Repeat struct {
	Rule           string `json:"rule"`
	Text           string `json:"text,omitempty"`
	FromCompletion bool   `json:"from_completion,omitempty"`
	Next           string `json:"next"`
	Zone           string `json:"zone,omitempty"`
	Start          string `json:"start,omitempty"`
	Completed      int    `json:"completed,omitempty"`
	Exhausted      bool   `json:"exhausted,omitempty"`
}

// Todo is the typed front matter of a to-do. Title and note stay in Markdown.
// Empty State on capture means open; other optional flow fields remain unset.
type Todo struct {
	State          string          `json:"state"`
	Checklist      []ChecklistItem `json:"checklist,omitempty"`
	When           string          `json:"when,omitempty"`
	Deadline       string          `json:"deadline,omitempty"`
	DeadlineSource string          `json:"deadline_source,omitempty"`
	Repeat         *Repeat         `json:"repeat,omitempty"`
	Important      bool            `json:"important,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	BelongsTo      []string        `json:"belongs_to,omitempty"`
	Source         string          `json:"source,omitempty"`
	BlockedBy      []string        `json:"blocked_by,omitempty"`
	WaitingOn      *WaitingOn      `json:"waiting_on,omitempty"`
	Assignee       *Assignee       `json:"assignee,omitempty"`
	PullRequest    string          `json:"pull_request,omitempty"`
	Issue          string          `json:"issue,omitempty"`
	ExecutionState string          `json:"execution_state,omitempty"`
	Priority       string          `json:"priority,omitempty"`
}

// Go's RFC3339 parser also accepts single-digit hours, comma fractions and
// overflowing zone components. SQLite's date functions cannot read those forms
// or offsets beyond 14:59. Preserve valid text, including fractional zeros.
var todoTimestamp = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-](0[0-9]|1[0-4]):[0-5][0-9])$`)

func validDate(value string) bool {
	if value == "" {
		return true
	}
	if d, err := time.Parse(time.DateOnly, value); err == nil && d.Format(time.DateOnly) == value {
		return true
	}
	if !todoTimestamp.MatchString(value) {
		return false
	}
	d, err := time.Parse(time.RFC3339, value)
	return err == nil && d.Year() > 0 && d.UTC().Year() <= 9999
}
func (v Todo) Validate() error {
	switch v.State {
	case "", "backlog", "open", "started", "completed", "canceled":
	default:
		return ErrInvalid
	}
	for _, date := range []string{v.When, v.Deadline} {
		if !validDate(date) {
			return ErrAmbiguousDate
		}
	}
	if v.Deadline != "" && (strings.TrimSpace(v.DeadlineSource) == "" || v.Repeat != nil) {
		return ErrInvalid
	}
	if v.Deadline == "" && v.DeadlineSource != "" {
		return ErrInvalid
	}
	if v.Repeat != nil && (strings.TrimSpace(v.Repeat.Rule) == "" || v.Repeat.Next == "") {
		return ErrInvalid
	}
	if v.Repeat != nil && !validDate(v.Repeat.Next) {
		return ErrAmbiguousDate
	}
	if v.Repeat != nil {
		if _, _, err := repeatSchedule(*v.Repeat); err != nil {
			return err
		}
	}
	if v.WaitingOn != nil {
		if strings.TrimSpace(v.WaitingOn.Who) == "" || v.WaitingOn.Since == "" || v.WaitingOn.NextCheck == "" {
			return ErrInvalid
		}
		if !validDate(v.WaitingOn.Since) || !validDate(v.WaitingOn.NextCheck) {
			return ErrAmbiguousDate
		}
	}
	if v.Assignee != nil {
		if v.Assignee.ID == "" {
			return ErrInvalid
		}
		switch v.Assignee.Kind {
		case "person", "agent", "worker":
		default:
			return ErrInvalid
		}
	}
	switch v.Priority {
	case "", "none", "normal", "high", "urgent":
	default:
		return ErrInvalid
	}
	for _, list := range [][]string{v.Tags, v.BelongsTo, v.BlockedBy} {
		seen := map[string]bool{}
		for _, value := range list {
			if strings.TrimSpace(value) == "" || seen[value] {
				return ErrInvalid
			}
			seen[value] = true
		}
	}
	for _, item := range v.Checklist {
		if strings.TrimSpace(item.Text) == "" {
			return ErrInvalid
		}
	}
	return nil
}

var todoKeys = strings.Fields("state checklist when deadline deadline_source repeat important tags belongs_to source blocked_by waiting_on assignee pull_request issue execution_state priority")

func parseTodo(fields map[string]string) (*Todo, error) {
	raw := map[string]json.RawMessage{}
	for _, key := range todoKeys {
		if value, ok := fields[key]; ok {
			// Existing front matter uses bare state codes.
			if key == "state" && !strings.HasPrefix(value, `"`) {
				b, _ := json.Marshal(value)
				value = string(b)
			}
			raw[key] = json.RawMessage(value)
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, ErrInvalid
	}
	var v Todo
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&v) != nil {
		return nil, ErrInvalid
	}
	if v.State == "" {
		v.State = "open"
	}
	normalizeLegacyImportedTodo(&v, fields)
	if err = v.Validate(); err != nil {
		return nil, err
	}
	return &v, nil
}

// WithTodo replaces just the known planning fields and keeps unrelated metadata.
func WithTodo(front string, v Todo) (string, error) {
	if v.State == "" {
		v.State = "open"
	}
	if err := v.Validate(); err != nil {
		return "", err
	}
	keep := []string{}
	keys := map[string]bool{}
	for _, key := range todoKeys {
		keys[key] = true
	}
	for _, line := range strings.Split(front, "\n") {
		key, _, _ := strings.Cut(line, ": ")
		if !keys[key] {
			keep = append(keep, line)
		}
	}
	b, _ := json.Marshal(v)
	var values map[string]json.RawMessage
	_ = json.Unmarshal(b, &values)
	names := []string{}
	for key := range values {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		keep = append(keep, key+": "+string(values[key]))
	}
	return strings.Join(keep, "\n"), nil
}

// Called with recordsMu held, after snapshot repair and before publication.
// The candidate replaces its own outgoing edges; no competing writer can pass
// this check against the same old graph and publish a cycle later.
func (s *Store) validateTodoLinks(ctx context.Context, id string, d document) error {
	if d.todo == nil {
		return nil
	}
	return s.db.Read(ctx, func(tx *canon.Tx) error {
		for _, target := range d.todo.BlockedBy {
			if target == id {
				return ErrDependencyCycle
			}
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM records_text WHERE id=? AND person=? AND kind='todo'", target, d.person).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return ErrInvalid
			}
			if err := tx.QueryRowContext(ctx, `WITH RECURSIVE ancestors(id) AS (
    SELECT ? UNION SELECT e.target FROM records_edges e JOIN ancestors a ON e.source=a.id WHERE e.relation='blocked_by'
   ) SELECT count(*) FROM ancestors WHERE id=?`, target, id).Scan(&count); err != nil {
				return err
			}
			if count > 0 {
				return ErrDependencyCycle
			}
		}
		for _, target := range d.todo.BelongsTo {
			var count int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM records_text WHERE id=? AND person=? AND kind IN ('project','area')", target, d.person).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return ErrInvalid
			}
		}
		return nil
	})
}

// Before C-54, the one-time tracker importer wrote active/done and kept the
// deadline's source only in provenance. Project those published records into
// the current shape without changing their canonical bytes or revision.
func normalizeLegacyImportedTodo(v *Todo, fields map[string]string) {
	if v.State != "active" && v.State != "done" {
		return
	}
	var source struct {
		Source string `json:"source"`
		Kind   string `json:"kind"`
		ID     string `json:"id"`
		Hash   string `json:"snapshot_sha256"`
	}
	if json.Unmarshal([]byte(fields["provenance"]), &source) != nil || source.Source != "tracker" || (source.Kind != "issue" && source.Kind != "milestone") || source.ID == "" {
		return
	}
	hash, err := hex.DecodeString(source.Hash)
	if err != nil || len(hash) != 32 {
		return
	}
	if v.State == "active" {
		v.State = "open"
	} else {
		v.State = "completed"
	}
	if v.Deadline != "" && v.DeadlineSource == "" {
		v.DeadlineSource = "snapshot:" + source.Hash + ":" + source.Source + ":" + source.Kind + ":" + source.ID
	}
}
