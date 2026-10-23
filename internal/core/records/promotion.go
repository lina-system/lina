// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type Applied struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Markdown string `json:"markdown"`
	Undo     string `json:"undo,omitempty"`
	Notice   string `json:"notice,omitempty"`
}
type recordOperation struct {
	Handle, Person, Agent, Notice string
	Undo                          bool
	Candidate                     int64
}

type RecordNotice struct{ Handle, Agent, Notice, UndoRevision string }

func (s *Store) Notices(ctx context.Context, person, after string) ([]RecordNotice, error) {
	out := []RecordNotice{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT handle,agent,notice,undo_revision FROM record_operations WHERE person=? AND handle>? ORDER BY handle LIMIT 100", person, after)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n RecordNotice
			if err = rows.Scan(&n.Handle, &n.Agent, &n.Notice, &n.UndoRevision); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

func checkpointOperation(ctx context.Context, tx *canon.Tx, p publication, id string) error {
	op := p.Operation
	if op == nil {
		return nil
	}
	if op.Undo {
		if _, err := tx.ExecContext(ctx, "UPDATE record_operations SET undo_revision=? WHERE handle=? AND person=? AND undo_revision=''", p.Commit, op.Handle, op.Person); err != nil {
			return err
		}
		if op.Candidate != 0 {
			_, err := tx.ExecContext(ctx, "UPDATE record_candidates SET status='rejected' WHERE number=? AND person=?", op.Candidate, op.Person)
			return err
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO record_operations(handle,person,agent,record_id,before_blob,revision,notice,candidate) VALUES(?,?,?,?,?,?,?,?)", op.Handle, op.Person, op.Agent, id, p.OldBlob, p.Commit, op.Notice, op.Candidate); err != nil {
		return err
	}
	if op.Candidate != 0 {
		_, err := tx.ExecContext(ctx, "UPDATE record_candidates SET status='added',record_id=?,undo_handle=? WHERE number=? AND person=?", id, op.Handle, op.Candidate, op.Person)
		return err
	}
	return nil
}

func (s *Store) ownedLocked(ctx context.Context, a Actor, id string) (Record, Rev, error) {
	head, files, err := s.snapshot(ctx)
	if err != nil {
		return Record{}, "", err
	}
	r, err := s.find(files, id)
	if err != nil {
		return r, "", err
	}
	d, err := parseDocument(r.Data)
	if err != nil {
		return r, "", err
	}
	if a.Person == "" || a.Agent == "" || d.person != a.Person {
		return Record{}, "", ErrNotFound
	}
	rev, err := s.recordRevision(ctx, head, r, files[r.Path])
	return r, rev, err
}

func (s *Store) PromoteMemo(ctx context.Context, a Actor, id, expected string) (Applied, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	r, rev, err := s.ownedLocked(ctx, a, id)
	if err != nil {
		return Applied{}, err
	}
	if expected == "" || rev != expected {
		return Applied{}, ErrStaleRevision
	}
	d, err := parseDocument(r.Data)
	if err != nil {
		return Applied{}, err
	}
	if d.kind != "memo" {
		return Applied{}, ErrInvalid
	}
	front, body, _ := strings.Cut(string(r.Data), "\n---\n")
	lines := strings.Split(front, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "kind: ") {
			lines[i] = "kind: todo"
		}
	}
	front, err = WithTodo(strings.Join(lines, "\n"), Todo{State: "open", Source: "memo:" + id})
	if err != nil {
		return Applied{}, err
	}
	data := []byte(front + "\n---\n" + body)
	op := &recordOperation{Handle: newID(), Person: a.Person, Agent: a.Agent, Notice: "Memo converted to a to-do."}
	rev, err = s.commitLocked(ctx, Change{ID: id, Base: rev, Path: r.Path, Data: data}, a, op)
	if err != nil {
		return Applied{}, err
	}
	return Applied{ID: id, Revision: rev, Markdown: string(data), Undo: op.Handle, Notice: op.Notice}, nil
}

func (s *Store) UndoRecord(ctx context.Context, a Actor, handle string, expected ...string) (Applied, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if _, _, err := s.snapshot(ctx); err != nil {
		return Applied{}, err
	}
	var id, before, revision, undone string
	var candidate int64
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT record_id,before_blob,revision,undo_revision,candidate FROM record_operations WHERE handle=? AND person=?", handle, a.Person).Scan(&id, &before, &revision, &undone, &candidate)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return Applied{}, ErrNotFound
	}
	if err != nil {
		return Applied{}, err
	}
	r, current, err := s.ownedLocked(ctx, a, id)
	if err != nil {
		return Applied{}, err
	}
	if len(expected) != 0 && (len(expected) != 2 || expected[0] != id || expected[1] != current) {
		return Applied{}, ErrStaleRevision
	}
	if undone != "" {
		return Applied{ID: id, Revision: current, Markdown: string(r.Data)}, nil
	}
	if current != revision {
		return Applied{}, ErrStaleRevision
	}
	var data []byte
	if before != "" {
		data, err = s.readObject(before, "blob")
	} else {
		d, e := parseDocument(r.Data)
		if e != nil || d.todo == nil {
			return Applied{}, ErrInvalid
		}
		d.todo.State = "canceled"
		front, body, _ := strings.Cut(string(r.Data), "\n---\n")
		front, err = WithTodo(front, *d.todo)
		data = []byte(front + "\n---\n" + body)
	}
	if err != nil {
		return Applied{}, err
	}
	op := &recordOperation{Handle: handle, Person: a.Person, Agent: a.Agent, Undo: true, Candidate: candidate}
	rev, err := s.commitLocked(ctx, Change{ID: id, Base: current, Path: r.Path, Data: data}, a, op)
	if err != nil {
		return Applied{}, err
	}
	return Applied{ID: id, Revision: rev, Markdown: string(data), Notice: "Change undone."}, nil
}
