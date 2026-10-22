// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/lease"
)

type Material struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}

// Boot supplies the assets adapter; L3 work never imports another L3 store.
type MaterialWriter interface {
	Prepare(context.Context, io.Reader, string, string) (MaterialWrite, error)
}
type MaterialWrite interface {
	Insert(context.Context, *canon.Tx) (Material, error)
	Close() error
}

func NewWithArea(db *canon.Store, area *Area, materials MaterialWriter) *Service {
	return &Service{db: db, area: area, materials: materials}
}

// Prepare records the allocation intent before touching Git. A failed operation
// can be retried with the identical definition, without resetting worker files.
func (s *Service) Prepare(ctx context.Context, l lease.Lease, t Task, spec WorkspaceSpec) (Workspace, error) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if s.area == nil || spec.validate(t.ID) != nil {
		return Workspace{}, ErrInvalid
	}
	w := Workspace{TaskID: t.ID, Path: filepath.Join(s.area.path, "tasks", t.ID), Spec: spec}
	t.Workspace = &w
	t.ArtifactRoots = []string{w.Path}
	if err := s.Create(ctx, t); err != nil {
		return Workspace{}, err
	}
	return s.area.Prepare(ctx, l, t.ID, spec)
}

// Retain copies a completed, unapplied no-PR delivery into materials. Copying
// proves byte identity, never the done condition or acceptance. The exact branch
// bundle accompanies Git results; a caller cannot replace a receipt's files with
// arbitrary paths. Material metadata and the work event commit atomically.
func (s *Service) Retain(ctx context.Context, l lease.Lease, taskID, receiptID, head string) (saved []Material, err error) {
	s.workspaceMu.Lock()
	defer s.workspaceMu.Unlock()
	if s.area == nil || s.materials == nil {
		return nil, ErrInvalid
	}
	c, err := s.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if c.Active || c.Task.Workspace == nil || c.Receipt == nil || c.Receipt.EventID != receiptID || c.Receipt.Outcome != "ready_for_review" || c.TurnStatus != "completed" {
		return nil, ErrState
	}
	if len(c.Materials) > 0 {
		if head != c.ResultHead {
			return nil, ErrState
		}
		return c.Materials, nil
	}
	w := *c.Task.Workspace
	if w.TaskID != taskID || w.Spec.validate(taskID) != nil || w.Path != filepath.Join(s.area.path, "tasks", taskID) {
		return nil, ErrInvalid
	}
	if w.Spec.RepositoryID == "" && head != "" {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(w.Path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var prepared []MaterialWrite
	defer func() {
		for _, p := range prepared {
			err = errors.Join(err, p.Close())
		}
	}()
	prepare := func(reader io.Reader, path, want string) error {
		hash := sha256.New()
		p, e := s.materials.Prepare(ctx, io.TeeReader(reader, hash), path, c.Task.AgentID)
		if e != nil {
			return e
		}
		prepared = append(prepared, p)
		if want != "" && fmt.Sprintf("%x", hash.Sum(nil)) != want {
			return ErrInvalid
		}
		return nil
	}
	for _, file := range c.Receipt.Manifest {
		rel, e := filepath.Rel(w.Path, file.Path)
		if e != nil || !filepath.IsLocal(rel) || strings.Contains("/"+filepath.ToSlash(rel)+"/", "/.git/") {
			return nil, ErrInvalid
		}
		f, e := root.Open(rel)
		if e != nil {
			return nil, e
		}
		info, e := f.Stat()
		if e == nil && !info.Mode().IsRegular() {
			e = ErrInvalid
		}
		if e == nil {
			e = prepare(f, file.Path, file.SHA256)
		}
		e = errors.Join(e, f.Close())
		if e != nil {
			return nil, e
		}
	}
	if w.Spec.RepositoryID != "" {
		bundle, e := s.area.Bundle(ctx, l, w, head)
		if e != nil {
			return nil, e
		}
		defer os.Remove(bundle)
		f, e := os.Open(bundle)
		if e != nil {
			return nil, e
		}
		e = prepare(f, filepath.Join(w.Path, "branch.bundle"), "")
		e = errors.Join(e, f.Close())
		if e != nil {
			return nil, e
		}
	}
	if len(prepared) == 0 {
		return nil, ErrInvalid
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, p := range prepared {
			m, e := p.Insert(ctx, tx)
			if e != nil {
				return e
			}
			saved = append(saved, m)
		}
		return appendEvent(ctx, tx, Event{Key: "retained:" + receiptID, Kind: "retained", TaskID: taskID, ReceiptID: receiptID, Materials: saved, Value: head})
	})
	if err != nil {
		return nil, err
	}
	return saved, nil
}
