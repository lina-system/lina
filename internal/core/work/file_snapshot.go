// SPDX-License-Identifier: Apache-2.0
package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var ErrFilesChanged = errors.New("result changed since done; inspect it and report done again")

// FileTree paths are relative to a bundle: out/ contains deliverables and
// report.md contains the worker's report. Directory names are fingerprinted too.
type FileTree struct {
	Files       []Artifact `json:"files"`
	Directories []string   `json:"directories"`
}

func (t FileTree) Fingerprint() string { b, _ := json.Marshal(t); return digest(string(b)) }

type FileSnapshot struct {
	Revision    int        `json:"revision"`
	Tree        FileTree   `json:"tree"`
	Fingerprint string     `json:"fingerprint"`
	Materials   []Material `json:"materials"`
}
type MaterialReader interface {
	Open(context.Context, Material) (io.ReadCloser, error)
}

type contextFileReader struct {
	ctx context.Context
	io.Reader
}

func (r contextFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func readTree(ctx context.Context, root *os.Root) (tree FileTree, err error) {
	for _, name := range []string{"out", "report.md"} {
		err = fs.WalkDir(root.FS(), name, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			info, e := entry.Info()
			if e != nil {
				return e
			}
			if info.IsDir() {
				if path == "report.md" {
					return ErrInvalid
				}
				tree.Directories = append(tree.Directories, path)
				return nil
			}
			if !info.Mode().IsRegular() || path == "out" {
				return ErrInvalid
			}
			f, e := root.Open(path)
			if e != nil {
				return e
			}
			h := sha256.New()
			n, e := io.Copy(h, contextFileReader{ctx, f})
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			if path == "report.md" && n == 0 {
				return ErrInvalid
			}
			tree.Files = append(tree.Files, Artifact{Path: path, SHA256: fmt.Sprintf("%x", h.Sum(nil)), Bytes: &n})
			return nil
		})
		if err != nil {
			return tree, err
		}
	}
	if len(tree.Files) < 2 {
		return tree, ErrInvalid
	}
	slices.SortFunc(tree.Files, func(a, b Artifact) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	slices.Sort(tree.Directories)
	return tree, nil
}

func (s *Service) sourceRoot(c Card) (*os.Root, error) {
	if s.area == nil || c.Task.Workspace == nil || c.Task.Workspace.Spec.RepositoryID != "" || !areaID(c.Task.ID) || c.Task.Workspace.Path != filepath.Join(s.area.path, "tasks", c.Task.ID) {
		return nil, ErrInvalid
	}
	info, err := s.area.root.Lstat(filepath.Join("tasks", c.Task.ID))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, ErrInvalid
	}
	return s.area.root.OpenRoot(filepath.Join("tasks", c.Task.ID))
}

// snapshotFiles runs at receipt admission, not at a later retention request.
// All material references and the observed fingerprint publish atomically.
func (s *Service) snapshotFiles(ctx context.Context, c Card, runID string, receipt Receipt) (err error) {
	if s.materials == nil || c.Run == nil || c.Run.ID != runID || c.Applied {
		return ErrState
	}
	if err = validateReceipt(c, receipt); err != nil {
		return err
	}
	root, err := s.sourceRoot(c)
	if err != nil {
		return err
	}
	defer root.Close()
	tree, err := readTree(ctx, root)
	if err != nil {
		return err
	}
	want := make([]Artifact, len(tree.Files))
	for i, f := range tree.Files {
		want[i] = Artifact{Path: filepath.Join(c.Task.Workspace.Path, f.Path), SHA256: f.SHA256}
	}
	if ManifestHash(want) != receipt.RevisionHash {
		return ErrFilesChanged
	}
	if c.Receipt != nil && c.Receipt.EventID == receipt.EventID {
		if c.FileSnapshot == nil || c.FileSnapshot.Fingerprint != tree.Fingerprint() {
			return ErrFilesChanged
		}
		return nil
	}
	var prepared []MaterialWrite
	defer func() {
		for _, p := range prepared {
			err = errors.Join(err, p.Close())
		}
	}()
	for _, file := range tree.Files {
		f, e := root.Open(file.Path)
		if e != nil {
			return e
		}
		h := sha256.New()
		p, e := s.materials.Prepare(ctx, io.TeeReader(contextFileReader{ctx, f}, h), file.Path, c.Task.AgentID)
		e = errors.Join(e, f.Close())
		if p != nil {
			prepared = append(prepared, p)
		}
		if e != nil {
			return e
		}
		if fmt.Sprintf("%x", h.Sum(nil)) != file.SHA256 {
			return ErrFilesChanged
		}
	}
	after, err := readTree(ctx, root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(tree, after) {
		return ErrFilesChanged
	}
	revision := 1
	if c.FileSnapshot != nil {
		revision = c.FileSnapshot.Revision + 1
	}
	snapshot := &FileSnapshot{Revision: revision, Tree: tree, Fingerprint: tree.Fingerprint()}
	return s.db.Write(ctx, func(tx *canon.Tx) error {
		for _, p := range prepared {
			m, e := p.Insert(ctx, tx)
			if e != nil {
				return e
			}
			snapshot.Materials = append(snapshot.Materials, m)
		}
		return appendEvent(ctx, tx, Event{Key: fmt.Sprintf("file-receipt:%s:%s:%d", c.Task.ID, runID, revision), TaskID: c.Task.ID, Kind: "file_receipt", RunID: runID, Receipt: &receipt, FileSnapshot: snapshot})
	})
}
