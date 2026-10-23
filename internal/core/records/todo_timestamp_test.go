// SPDX-License-Identifier: Apache-2.0
package records

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var todoDateFields = map[string]func(string) Todo{
	"when":        func(v string) Todo { return Todo{When: v} },
	"deadline":    func(v string) Todo { return Todo{Deadline: v, DeadlineSource: "mail:1"} },
	"repeat.next": func(v string) Todo { return Todo{Repeat: &Repeat{Rule: "FREQ=DAILY", Next: v}} },
	"waiting_on.since": func(v string) Todo {
		return Todo{WaitingOn: &WaitingOn{Who: "supplier", Since: v, NextCheck: "2026-10-23"}}
	},
	"waiting_on.next_check": func(v string) Todo {
		return Todo{WaitingOn: &WaitingOn{Who: "supplier", Since: "2026-10-21", NextCheck: v}}
	},
}

func TestTodoTimestampSQLiteRange(t *testing.T) {
	_, _, db := setup(t)
	for _, value := range []string{
		"0001-01-01T00:00:00+14:59",
		"9999-12-31T23:59:59.999Z",
		"9999-12-31T23:59:59.999999999Z",
	} {
		t.Run(value, func(t *testing.T) {
			if !validDate(value) {
				t.Fatal("valid timestamp was rejected")
			}
			if err := db.Read(t.Context(), func(tx *canon.Tx) error {
				var parsed bool
				if err := tx.QueryRowContext(t.Context(), "SELECT julianday(?) IS NOT NULL", value).Scan(&parsed); err != nil {
					return err
				}
				if !parsed {
					t.Fatal("accepted timestamp cannot be parsed by SQLite")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTodoNonCanonicalTimestamps(t *testing.T) {
	for field, todo := range todoDateFields {
		t.Run(field, func(t *testing.T) {
			for _, value := range []string{
				"2026-10-22T9:00:00+09:00",
				"2026-10-22T09:00:00,5+09:00",
				"2026-10-22T09:00:00+24:00",
				"2026-10-22T09:00:00+09:60",
				"2026-10-22T09:00:00+15:00",
				"2026-10-22T09:00:00-15:00",
				"9999-12-31T23:00:00-01:00",
			} {
				t.Run(value, func(t *testing.T) {
					if err := todo(value).Validate(); !errors.Is(err, ErrAmbiguousDate) {
						t.Fatalf("Validate(%q) = %v; want ambiguous_date", value, err)
					}
				})
			}
		})
	}
}

func TestTodoNonCanonicalTimestampDoesNotPublish(t *testing.T) {
	for field, todo := range todoDateFields {
		t.Run(field, func(t *testing.T) {
			s, _, db := setup(t)
			base := commit(t, s, Change{ID: "saved", Path: "inbox/saved.md", Data: todoDocument("saved", "")})
			before := indexDump(t, db)
			// Construct raw front matter so this tests the publication boundary,
			// independently of WithTodo's validation of tool input.
			v := todo("2026-10-22T9:00:00+09:00")
			fields := map[string]string{
				"when":                  fmt.Sprintf("when: %q\n", v.When),
				"deadline":              fmt.Sprintf("deadline: %q\ndeadline_source: \"mail:1\"\n", v.Deadline),
				"repeat.next":           "repeat: {\"rule\":\"FREQ=DAILY\",\"next\":\"2026-10-22T9:00:00+09:00\"}\n",
				"waiting_on.since":      "waiting_on: {\"who\":\"supplier\",\"since\":\"2026-10-22T9:00:00+09:00\",\"next_check\":\"2026-10-23\"}\n",
				"waiting_on.next_check": "waiting_on: {\"who\":\"supplier\",\"since\":\"2026-10-21\",\"next_check\":\"2026-10-22T9:00:00+09:00\"}\n",
			}[field]
			for _, c := range []Change{
				{ID: "new", Path: "inbox/new.md", Data: todoDocument("new", fields)},
				{ID: "saved", Path: "inbox/saved.md", Base: base, Data: todoDocument("saved", fields)},
			} {
				if _, err := s.Commit(t.Context(), c, actor); !errors.Is(err, ErrAmbiguousDate) {
					t.Fatalf("Commit(%s) = %v; want ambiguous_date", c.ID, err)
				}
			}
			if head, err := s.Head(t.Context()); err != nil || head != base {
				t.Fatal("refusal changed history", head, err)
			}
			if after := indexDump(t, db); after != before {
				t.Fatal("refusal changed the index")
			}
			if r, rev, err := s.Read(t.Context(), "saved"); err != nil || rev != base || string(r.Data) != string(todoDocument("saved", "")) {
				t.Fatal("refusal changed the saved record", r, rev, err)
			}
			if _, _, err := s.Read(t.Context(), "new"); !errors.Is(err, ErrNotFound) {
				t.Fatal("refused record was published", err)
			}
		})
	}
}

func TestTodoValidTimestampsRemainQueryable(t *testing.T) {
	s, _, _ := setup(t)
	want := map[string]Todo{}
	for field, todo := range todoDateFields {
		for i, value := range []string{
			"2026-10-22",
			"2026-10-22T09:00:00+09:00",
			"2026-10-22T09:00:00.000+09:00",
			"2026-10-22T09:00:00.123456789123+09:00",
			"2026-10-22T00:00:00Z",
			"2026-10-22T00:00:00+00:00",
			"2026-10-22T00:00:00-00:00",
			"2026-10-22T04:00:00-06:00",
			"2026-10-22T09:00:00+14:59",
		} {
			id := fmt.Sprintf("%s-%02d", field, i)
			v := todo(value)
			v.State = "open"
			if v.WaitingOn != nil {
				v.When = value
			}
			front, err := WithTodo(fmt.Sprintf("---\nid: %q\nkind: todo\nperson_id: \"person\"", id), v)
			if err != nil {
				t.Fatalf("%s %q: %v", field, value, err)
			}
			commit(t, s, Change{ID: id, Path: "inbox/" + id + ".md", Data: []byte(front + "\n---\n\n# Task\n")})
			want[id] = v
		}
	}
	for _, view := range []string{"board", "list", "today", "week", "upcoming"} {
		t.Run(view, func(t *testing.T) {
			day := "2026-10-22"
			if view == "upcoming" {
				day = "2026-10-21"
			}
			rows, err := s.Query(t.Context(), Params{Person: "person", View: view, Day: day, Zone: "Asia/Seoul", Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]Todo{}
			for _, row := range rows {
				if row.Todo == nil || row.Overdue {
					t.Fatalf("invalid row: %+v", row)
				}
				got[row.ID] = *row.Todo
			}
			if !reflect.DeepEqual(got, want) || len(rows) != len(want) {
				t.Fatalf("%s lost or changed valid to-dos: got %v; want %v", view, got, want)
			}
		})
	}
}
