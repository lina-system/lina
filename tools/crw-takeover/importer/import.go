// SPDX-License-Identifier: Apache-2.0
// Package importer is an operator-only migration. Product packages must never
// import it or resolve its source identities.
package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/thisisjun786/lina/internal/core/records"
)

// Import consumes exactly one verified snapshot. The caller must exclusively
// own the state root and use the same private journal for every retry.
func Import(ctx context.Context, repo records.Repo, actor records.Actor, data []byte, hash, journalPath string) (Report, error) {
	report := Report{Version: 1, SHA256: hash, Rows: []Row{}}
	s, err := verified(data, hash)
	if err != nil {
		return report, err
	}
	rows := make([]sourceRecord, len(s.Records))
	kinds := make([]string, len(rows))
	positions := map[string]int{}
	for i, raw := range s.Records {
		r, kind, reason := classify(raw)
		rows[i], kinds[i] = r, kind
		outcome := "lost"
		if kind == "unsupported" {
			outcome = kind
		}
		report.Rows = append(report.Rows, Row{Index: i, Ref: r.Ref, Outcome: outcome, Reason: reason})
		if reason != "" {
			continue
		}
		key := r.Ref.key()
		if prior, ok := positions[key]; ok {
			kinds[i], kinds[prior] = "lost", "lost"
			report.Rows[i].Reason = "duplicate source identity"
			report.Rows[prior].Reason = "duplicate source identity"
		} else {
			positions[key] = i
		}
	}
	failAll := func(err error) (Report, error) {
		for i := range report.Rows {
			if report.Rows[i].Reason == "" {
				report.Rows[i].Reason = err.Error()
			}
		}
		return report, errors.Join(err, report.Validate(len(rows)))
	}
	if actor.Person == "" || actor.Agent == "" {
		return failAll(errors.New("person and agent are required"))
	}
	j, err := loadJournal(journalPath, hash, actor)
	if err != nil {
		return failAll(err)
	}
	// Reserve all identities before resolving forward references or publishing.
	for i, r := range rows {
		if report.Rows[i].Reason != "" {
			continue
		}
		key := r.Ref.key()
		if _, ok := j.Entries[key]; !ok {
			j.Entries[key] = reservation{ID: records.NewID()}
		}
	}
	if err = SaveJSON(journalPath, j); err != nil {
		return failAll(err)
	}
	visited := make([]int, len(rows))
	var publish func(int) error
	publish = func(i int) error {
		out := &report.Rows[i]
		r := rows[i]
		if visited[i] == 2 {
			if out.Outcome != "imported" {
				return errors.New(out.Reason)
			}
			return nil
		}
		if visited[i] == 1 {
			return errors.New("cyclic source bindings")
		}
		if out.Reason != "" {
			visited[i] = 2
			return errors.New(out.Reason)
		}
		visited[i] = 1
		key := r.Ref.key()
		entry := j.Entries[key]
		out.LINAID = entry.ID
		loss := func(err error) error { out.Outcome = "lost"; out.Reason = err.Error(); visited[i] = 2; return err }
		links := map[string]string{}
		for _, link := range []struct {
			name string
			ref  *Ref
		}{{"parent_id", r.Parent}, {"project_id", r.Project}} {
			if link.ref == nil {
				continue
			}
			target, ok := positions[link.ref.key()]
			if !ok {
				return loss(errors.New("source binding has no imported target"))
			}
			if err := publish(target); err != nil {
				return loss(fmt.Errorf("source binding unavailable: %w", err))
			}
			links[link.name] = report.Rows[target].LINAID
		}
		change := convert(r, kinds[i], entry.ID, i, links, s.Records[i], hash, actor)
		existing, _, readErr := repo.Read(ctx, entry.ID)
		reused := readErr == nil
		if entry.Published {
			if readErr != nil {
				return loss(fmt.Errorf("previously imported record unavailable: %w", readErr))
			}
			// A later LINA edit is not permission to overwrite it from the old snapshot.
		} else if readErr == nil {
			if !bytes.Equal(existing.Data, change.Data) && !matchesLegacyTodo(existing.Data, change.Data, kinds[i]) {
				return loss(errors.New("reserved LINA id has different content"))
			}
		} else if !errors.Is(readErr, records.ErrNotFound) {
			return loss(readErr)
		} else if _, err := repo.Commit(ctx, change, actor); err != nil {
			return loss(err)
		}
		entry.Published = true
		j.Entries[key] = entry
		if err := SaveJSON(journalPath, j); err != nil {
			return loss(err)
		}
		out.Outcome = "imported"
		out.Reused = reused
		out.Reason = ""
		visited[i] = 2
		return nil
	}
	for i := range rows {
		_ = publish(i)
	}
	err = report.Validate(len(rows))
	for _, row := range report.Rows {
		if row.Outcome == "lost" {
			err = errors.Join(err, fmt.Errorf("record %d lost: %s", row.Index, row.Reason))
		}
	}
	return report, err
}

// An older importer may have published a to-do before acknowledging its journal
// entry. Reconstruct only that exact encoding from the verified snapshot. Never
// normalize the existing record: that could conceal a later edit or a collision.
func matchesLegacyTodo(existing, current []byte, kind string) bool {
	if kind != "todo" {
		return false
	}
	front, body, ok := strings.Cut(string(current), "\n---\n")
	if !ok {
		return false
	}
	var lines []string
	for _, line := range strings.Split(front, "\n") {
		switch line {
		case "state: open":
			line = "state: active"
		case "state: completed":
			line = "state: done"
		}
		if !strings.HasPrefix(line, "deadline_source: ") {
			lines = append(lines, line)
		}
	}
	return string(existing) == strings.Join(lines, "\n")+"\n---\n"+body
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }
func convert(r sourceRecord, kind, id string, index int, links map[string]string, raw json.RawMessage, hash string, actor records.Actor) records.Change {
	state := "active"
	if r.Done {
		state = "done"
	}
	if kind == "todo" {
		state = "open"
		if r.Done {
			state = "completed"
		}
	}
	if kind == "goal" {
		state = "draft"
	}
	if r.Source == "crw" {
		state = "observed"
	}
	front := fmt.Sprintf("---\nid: %s\nkind: %s\nstate: %s\nperson_id: %s\nagent_id: %s\n", quote(id), kind, state, quote(actor.Person), quote(actor.Agent))
	var belongs []string
	for _, key := range []string{"parent_id", "project_id"} {
		if links[key] != "" {
			front += key + ": " + quote(links[key]) + "\n"
			if len(belongs) == 0 || belongs[0] != links[key] {
				belongs = append(belongs, links[key])
			}
		}
	}
	if len(belongs) > 0 {
		raw, _ := json.Marshal(belongs)
		front += "belongs_to: " + string(raw) + "\n"
	}
	for _, f := range []struct{ key, value string }{{"done_condition", r.DoneCondition}, {"deadline", r.Deadline}, {"intent", r.Intent}} {
		if f.value != "" {
			front += f.key + ": " + quote(f.value) + "\n"
		}
	}
	if kind == "todo" && r.Deadline != "" {
		front += "deadline_source: " + quote("snapshot:"+hash+":"+r.Source+":"+r.Kind+":"+r.ID) + "\n"
	}
	provenance, _ := json.Marshal(struct {
		Ref
		SHA256 string `json:"snapshot_sha256"`
	}{r.Ref, hash})
	front += "provenance: " + string(provenance) + "\n---\n\n# " + r.Title + "\n\n" + r.Body + "\n\n## Source observation\n\n"
	// Indented JSON is data, including any Markdown fences in source text.
	var pretty bytes.Buffer
	_ = json.Indent(&pretty, raw, "", "  ")
	front += "    " + strings.ReplaceAll(pretty.String(), "\n", "\n    ") + "\n"
	folder := "resources"
	if kind == "project" || kind == "todo" {
		folder = "projects"
	}
	if kind == "goal" {
		folder = "inbox"
	}
	return records.Change{ID: id, Path: fmt.Sprintf("%s/%s-takeover-%d.md", folder, slug(r.Title), index+1), Data: []byte(front)}
}
func slug(title string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(title) {
		if unicode.IsLetter(c) || unicode.IsDigit(c) {
			b.WriteRune(c)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() > 70 {
			break
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "record"
	}
	return result
}
