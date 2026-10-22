// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
)

// OutsideChange exposes classifications, never untrusted bytes or private paths.
type OutsideChange struct {
	Path       string `json:"-"`
	Kind       string `json:"kind"`
	Validation string `json:"validation"`
}
type OutsideError struct {
	Changes []OutsideChange `json:"changes"`
	Total   int             `json:"total"`
}

func (*OutsideError) Error() string { return ErrRecordsChanged.Error() }
func (*OutsideError) Unwrap() error { return ErrRecordsChanged }

func recordFile(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, ErrInvalid
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if len(data) > 2<<20 {
		return nil, ErrInvalid
	}
	return data, err
}
func (s *Store) outside(ctx context.Context, files map[string]string, repositoryChanged bool) error {
	notices := map[string]OutsideChange{}
	if repositoryChanged {
		notices[".git/HEAD"] = OutsideChange{Path: ".git/HEAD", Kind: "repository", Validation: "unaccepted"}
	}
	if _, err := s.root.Lstat(".git/index.lock"); err == nil {
		notices[".git/index.lock"] = OutsideChange{Path: ".git/index.lock", Kind: "repository", Validation: "locked"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seen := map[string]bool{}
	identities := map[string]string{}
	err := fs.WalkDir(s.root.FS(), ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if name == ".git" {
			return fs.SkipDir
		}
		if e.IsDir() {
			return nil
		}
		seen[name] = true
		kind := "modified"
		if _, ok := files[name]; !ok {
			kind = "added"
		}
		n := OutsideChange{Path: name, Kind: kind, Validation: "valid"}
		if !validPath(name) || !e.Type().IsRegular() {
			n.Validation = "invalid"
			notices[name] = n
			return nil
		}
		data, err := recordFile(s.root, name)
		if errors.Is(err, ErrInvalid) {
			n.Validation = "invalid"
			notices[name] = n
			return nil
		}
		if err != nil {
			return err
		}
		hash, _ := objectID("blob", data)
		id, idErr := recordID(data)
		if idErr != nil {
			n.Validation = "invalid"
		} else if prior, ok := identities[id]; ok {
			n.Validation = "duplicate_id"
			if other, ok := notices[prior]; ok {
				other.Validation = "duplicate_id"
				notices[prior] = other
			}
		} else {
			identities[id] = name
		}
		if hash != files[name] {
			if _, err := parseDocument(data); err != nil {
				n.Validation = "invalid"
			}
			notices[name] = n
		} else if n.Validation != "valid" {
			notices[name] = n
		}
		return nil
	})
	if err != nil {
		return err
	}
	for name := range files {
		if !seen[name] {
			notices[name] = OutsideChange{Path: name, Kind: "deleted", Validation: "missing"}
		}
	}
	if len(notices) == 0 {
		return nil
	}
	paths := make([]string, 0, len(notices))
	for name := range notices {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	out := &OutsideError{Total: len(paths)}
	for _, name := range paths[:min(len(paths), 100)] {
		out.Changes = append(out.Changes, notices[name])
	}
	return out
}
