// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"database/sql"
	"errors"
	"maps"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// A planning publication validates links against its complete candidate, so a
// new project or prerequisite can be referenced before any file is published.
func (s *Store) validateTodoBatch(ctx context.Context, candidates map[string]document) error {
	if len(candidates) == 1 {
		for id, d := range candidates {
			return s.validateTodoLinks(ctx, id, d)
		}
	}
	return s.db.Read(ctx, func(tx *canon.Tx) error {
		cache := maps.Clone(candidates)
		lookup := func(id string) (document, error) {
			if d, ok := cache[id]; ok {
				return d, nil
			}
			var d document
			if err := tx.QueryRowContext(ctx, "SELECT person,kind FROM records_text WHERE id=?", id).Scan(&d.person, &d.kind); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return d, ErrInvalid
				}
				return d, err
			}
			rows, err := tx.QueryContext(ctx, "SELECT target FROM records_edges WHERE source=? AND relation='blocked_by'", id)
			if err != nil {
				return d, err
			}
			defer rows.Close()
			for rows.Next() {
				var target string
				if err := rows.Scan(&target); err != nil {
					return d, err
				}
				d.blocked = append(d.blocked, target)
			}
			if err := rows.Err(); err != nil {
				return d, err
			}
			cache[id] = d
			return d, nil
		}
		visiting, checked := map[string]bool{}, map[string]bool{}
		var visit func(string) error
		visit = func(id string) error {
			if visiting[id] {
				return ErrDependencyCycle
			}
			if checked[id] {
				return nil
			}
			d, err := lookup(id)
			if err != nil {
				return err
			}
			visiting[id] = true
			for _, target := range d.blocked {
				if err := visit(target); err != nil {
					return err
				}
			}
			delete(visiting, id)
			checked[id] = true
			return nil
		}
		for id, d := range candidates {
			if d.todo == nil {
				continue
			}
			for _, target := range d.todo.BlockedBy {
				other, err := lookup(target)
				if err != nil {
					return err
				}
				if other.person != d.person || other.kind != "todo" {
					return ErrInvalid
				}
			}
			for _, target := range d.todo.BelongsTo {
				other, err := lookup(target)
				if err != nil {
					return err
				}
				if other.person != d.person || (other.kind != "project" && other.kind != "area") {
					return ErrInvalid
				}
			}
			if err := visit(id); err != nil {
				return err
			}
		}
		return nil
	})
}
