// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/recall"
)

// Params is scoped by Core, never by an untrusted caller's person argument.
// After is an exclusive id cursor; changing the repository starts a fresh view.
type Params struct {
	Person, Kind, Search, After string
	View, Day, Zone             string
	Limit                       int
}
type Row struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Title    string `json:"title"`
	Note     string `json:"note"`
	Todo     *Todo  `json:"todo,omitempty"`
	Overdue  bool   `json:"overdue,omitempty"`
	Blocked  bool   `json:"blocked,omitempty"`
}
type Index interface {
	Query(context.Context, Params) ([]Row, error)
}
type document struct {
	person, kind, state, title, note, source string
	belongs, blocked                         []string
	todo                                     *Todo
}

func parseDocument(data []byte) (document, error) {
	var d document
	if _, err := recordID(data); err != nil {
		return d, err
	}
	front, body, _ := strings.Cut(string(data[4:]), "\n---\n")
	fields, err := frontFields(front)
	if err != nil {
		return d, err
	}
	scalar := func(key string) (string, error) {
		value := fields[key]
		if strings.HasPrefix(value, `"`) {
			var s string
			if err := json.Unmarshal([]byte(value), &s); err != nil {
				return "", ErrInvalid
			}
			return s, nil
		}
		return value, nil
	}
	for key, dest := range map[string]*string{"person_id": &d.person, "kind": &d.kind, "state": &d.state, "source": &d.source} {
		*dest, err = scalar(key)
		if err != nil {
			return d, err
		}
	}
	for key, dest := range map[string]*[]string{"belongs_to": &d.belongs, "blocked_by": &d.blocked} {
		if raw, ok := fields[key]; ok {
			if json.Unmarshal([]byte(raw), dest) != nil {
				return d, ErrInvalid
			}
			for _, id := range *dest {
				if id == "" {
					return d, ErrInvalid
				}
			}
		}
	}
	if d.kind == "todo" {
		d.todo, err = parseTodo(fields)
		if err != nil {
			return d, err
		}
		d.state = d.todo.State
	}
	body = strings.TrimSpace(body)
	if strings.HasPrefix(body, "# ") {
		d.title, d.note, _ = strings.Cut(body[2:], "\n")
		d.note = strings.TrimSpace(d.note)
	} else {
		d.note = body
	}
	return d, nil
}

func putProjection(ctx context.Context, tx *canon.Tx, r indexRow) error {
	// Record citations are a disposable projection of the current file revision.
	if _, err := tx.ExecContext(ctx, "DELETE FROM cite WHERE view_kind='record' AND view_id=?", r.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM records_text WHERE id=?", r.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM records_edges WHERE source=?", r.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM records_todo WHERE id=?", r.id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO records_activity(id,last_activity) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET last_activity=excluded.last_activity", r.id, r.at.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	d := r.doc
	if d.source != "" {
		ref := canon.Citation{TargetKind: "external", TargetID: d.source, TargetRevision: "0", Precision: "none", Role: "cites"}
		var event int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM conv_event WHERE id=?", d.source).Scan(&event); err != nil {
			return err
		}
		if event == 1 {
			ref.TargetKind, ref.Precision = "event", "event"
		}
		if err := canon.ReplaceCitations(ctx, tx, "record", r.id, r.revision, []canon.Citation{ref}); err != nil {
			return err
		}
	}
	if d.todo != nil {
		if err := canon.RecordDoneCompletion(ctx, tx, d.todo.Completion); err != nil {
			return err
		}
		b, _ := json.Marshal(d.todo)
		next := ""
		if d.todo.Repeat != nil && !d.todo.Repeat.Exhausted {
			next = d.todo.Repeat.Next
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO records_todo(id,fields,when_value,deadline,repeat_next) VALUES(?,?,?,?,?)", r.id, string(b), d.todo.When, d.todo.Deadline, next); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO records_text(id,person,kind,state,title,note) VALUES(?,?,?,?,?,?)", r.id, d.person, d.kind, d.state, d.title, d.note); err != nil {
		return err
	}
	for relation, ids := range map[string][]string{"belongs_to": d.belongs, "blocked_by": d.blocked} {
		for _, target := range ids {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO records_edges(source,relation,target) VALUES(?,?,?)", r.id, relation, target); err != nil {
				return err
			}
		}
	}
	return recall.IndexText(ctx, tx, "record", r.id, d.title+"\n"+d.note)
}

func (s *Store) ensureIndex(ctx context.Context, head string, files map[string]string) error {
	var indexed string
	var count, texts, todos, projected int
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT head,(SELECT count(*) FROM records_index),(SELECT count(*) FROM records_text),(SELECT count(*) FROM records_text WHERE kind='todo'),(SELECT count(*) FROM records_todo) FROM records_index_state WHERE singleton=1").Scan(&indexed, &count, &texts, &todos, &projected)
	})
	if err != nil {
		return err
	}
	if indexed == head && count == len(files) && texts == count && todos == projected {
		return nil
	}
	return s.rebuild(ctx, head)
}
func (s *Store) rebuild(ctx context.Context, head string) error {
	rows, err := s.indexRows(ctx, head)
	if err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		if err := replaceIndex(ctx, tx, rows); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE records_index_state SET head=? WHERE singleton=1", head)
		return err
	})
}
func (s *Store) Query(ctx context.Context, p Params) ([]Row, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if p.Person == "" || p.Limit < 0 || p.Limit > 100 || len(p.Search) > 512 {
		return nil, ErrInvalid
	}
	bounds, err := viewBounds(p)
	if err != nil {
		return nil, err
	}
	if p.Limit == 0 {
		p.Limit = 50
	}
	if _, _, err := s.snapshot(ctx); err != nil {
		return nil, err
	}
	rows := []Row{}
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		query := "WITH bounds AS (SELECT ? AS day,? AS start,? AS end,? AS week_end,? AS week_day) SELECT i.id,i.revision,t.kind,t.state,t.title,t.note,COALESCE(v.fields,'')," +
			"(t.kind='todo' AND t.state NOT IN ('completed','canceled') AND " + dateBefore("v.when_value", "b.day", "b.start") + ")," +
			"(t.kind='todo' AND t.state NOT IN ('completed','canceled') AND EXISTS(SELECT 1 FROM records_edges e JOIN records_text blocker ON blocker.id=e.target WHERE e.source=i.id AND e.relation='blocked_by' AND blocker.person=t.person AND blocker.state NOT IN ('completed','canceled')))" +
			" FROM records_index i JOIN records_text t ON t.id=i.id LEFT JOIN records_todo v ON v.id=i.id CROSS JOIN bounds b WHERE t.person=? AND i.id>? AND " + viewFilter(p.View)
		args := append(bounds, p.Person, p.After)
		if p.Kind != "" {
			query += " AND t.kind=?"
			args = append(args, p.Kind)
		}
		if search := strings.TrimSpace(p.Search); search != "" {
			// Quote the whole phrase; model text cannot inject FTS query operators.
			switch {
			case len(canon.HangulBigrams(search)) == 0:
				query += " AND i.id IN (SELECT id FROM records_fts WHERE records_fts MATCH ?)"
				args = append(args, canon.TrigramPhrase(search))
			case utf8.RuneCountInString(search) == 2:
				query += " AND i.id IN (SELECT id FROM records_korean_bigram WHERE gram=?)"
				args = append(args, search)
			default:
				query += " AND i.id IN (SELECT id FROM records_korean_fts WHERE records_korean_fts MATCH ?)"
				args = append(args, canon.TrigramPhrase(search))
			}
		}
		query += " ORDER BY i.id LIMIT ?"
		args = append(args, p.Limit)
		rs, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r Row
			var todo string
			if err := rs.Scan(&r.ID, &r.Revision, &r.Kind, &r.State, &r.Title, &r.Note, &todo, &r.Overdue, &r.Blocked); err != nil {
				return err
			}
			if todo != "" {
				if err := json.Unmarshal([]byte(todo), &r.Todo); err != nil {
					return err
				}
			}
			if p.Day == "" {
				r.Overdue = false
			}
			rows = append(rows, r)
		}
		return rs.Err()
	})
	return rows, err
}
