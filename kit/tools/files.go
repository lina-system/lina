// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
)

const AbsentRevision = "absent"
const MaxFileBytes = 8 << 20

var (
	ErrFileRevision = errors.New("stale_revision")
	ErrFileScope    = errors.New("file_outside_scope")
	ErrFileSize     = errors.New("file_too_large")
)

type FileRequest struct {
	Operation string `json:"operation"`
	Path      string `json:"path,omitempty"`
	Revision  string `json:"revision,omitempty"`
	Content   []byte `json:"content,omitempty"`
	Patch     string `json:"patch,omitempty"`
	Version   string `json:"version,omitempty"`
}
type FileReceipt struct {
	Path    string `json:"path"`
	Before  string `json:"before"`
	After   string `json:"after"`
	Version string `json:"version"`
}
type FileResult struct {
	Content  []byte        `json:"content,omitempty"`
	Revision string        `json:"revision"`
	Changes  []FileReceipt `json:"changes,omitempty"`
}
type snapshot struct {
	Content []byte
	Mode    os.FileMode
	Absent  bool
}

func (s snapshot) revision() string {
	if s.Absent {
		return AbsentRevision
	}
	h := sha256.New()
	fmt.Fprintf(h, "%o\x00", s.Mode.Perm())
	h.Write(s.Content)
	return hex.EncodeToString(h.Sum(nil))
}

type savedVersion struct {
	Path                          string
	Before, After                 snapshot
	BeforeRevision, AfterRevision string
}

// Files is the file worker implementation, invoked inside the OS sandbox by
// Core's executor. Opening a Root is defense in depth, not an OS sandbox. The
// caller supplies a dedicated private version directory, never the state root.
// Core serializes calls for a root across worker processes through its registry.
type Files struct {
	mu             sync.Mutex
	root, versions *os.Root
}

func OpenFiles(root, versions string) (*Files, error) {
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	v, err := os.OpenRoot(versions)
	if err != nil {
		r.Close()
		return nil, err
	}
	info, err := v.Stat(".")
	if err != nil || info.Mode().Perm()&0077 != 0 {
		r.Close()
		v.Close()
		return nil, ErrFileScope
	}
	return &Files{root: r, versions: v}, nil
}
func (f *Files) Close() error { return errors.Join(f.root.Close(), f.versions.Close()) }

// parent pins the destination directory for the full read/check/write sequence.
// Root prevents escapes and the sandbox separately makes .git read-only.
func (f *Files) parent(name string, create bool) (*os.Root, string, error) {
	if !FilePath(name) {
		return nil, "", ErrFileScope
	}
	parts := strings.Split(name, "/")
	dir, err := f.root.OpenRoot(".")
	if err != nil {
		return nil, "", err
	}
	for _, part := range parts[:len(parts)-1] {
		info, err := dir.Lstat(part)
		if errors.Is(err, os.ErrNotExist) && create {
			err = dir.Mkdir(part, 0700)
			if err == nil {
				info, err = dir.Lstat(part)
			}
		}
		if err != nil {
			dir.Close()
			return nil, "", err
		}
		if !info.IsDir() {
			dir.Close()
			return nil, "", ErrFileScope
		}
		next, err := dir.OpenRoot(part)
		dir.Close()
		if err != nil {
			return nil, "", err
		}
		dir = next
	}
	return dir, parts[len(parts)-1], nil
}
func readSnapshot(dir *os.Root, name string) (snapshot, error) {
	info, err := dir.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot{Absent: true}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return snapshot{}, ErrFileScope
	}
	file, err := openFileNoFollow(dir, name)
	if err != nil {
		return snapshot{}, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return snapshot{}, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return snapshot{}, ErrFileScope
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxFileBytes+1))
	if err != nil {
		return snapshot{}, err
	}
	if len(data) > MaxFileBytes {
		return snapshot{}, ErrFileSize
	}
	return snapshot{Content: data, Mode: opened.Mode().Perm()}, nil
}
func (f *Files) read(name string) (snapshot, error) {
	dir, base, err := f.parent(name, false)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot{Absent: true}, nil
	}
	if err != nil {
		return snapshot{}, err
	}
	defer dir.Close()
	return readSnapshot(dir, base)
}
func syncDir(dir *os.Root) error {
	d, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func stage(dir *os.Root, data []byte, mode os.FileMode) (string, error) {
	name := ".lina-" + rand.Text()
	file, err := dir.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return "", err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Chmod(mode.Perm())
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		dir.Remove(name)
		return "", err
	}
	return name, nil
}
func (f *Files) save(version savedVersion) (string, error) {
	version.BeforeRevision = version.Before.revision()
	version.AfterRevision = version.After.revision()
	data, err := json.Marshal(version)
	if err != nil {
		return "", err
	}
	name, err := stage(f.versions, data, 0600)
	if err != nil {
		return "", err
	}
	if err := syncDir(f.versions); err != nil {
		return "", err
	}
	return name, nil
}
func (f *Files) load(name, target string) (snapshot, error) {
	if !strings.HasPrefix(name, ".lina-") || path.Base(name) != name || !FilePath(name) {
		return snapshot{}, ErrInvalid
	}
	file, err := openFileNoFollow(f.versions, name)
	if err != nil {
		return snapshot{}, err
	}
	defer file.Close()
	var v savedVersion
	decoder := json.NewDecoder(io.LimitReader(file, 3*MaxFileBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&v); err != nil {
		return snapshot{}, err
	}
	if v.Path != target || len(v.Before.Content) > MaxFileBytes || v.BeforeRevision != v.Before.revision() || v.AfterRevision != v.After.revision() {
		return snapshot{}, ErrInvalid
	}
	return v.Before, nil
}

// Execute checks every patch context before effects. Each write rechecks the
// snapshot at the destination, retains and fsyncs both versions, then replaces
// by rename. An interrupted multi-file commit is unknown with recoverable
// receipts; it is never reported as an atomic multi-file transaction.
func (f *Files) Execute(ctx context.Context, req FileRequest) (FileResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var result FileResult
	if err := ValidateFileRequest(req); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	before := map[string]snapshot{}
	after := map[string]snapshot{}
	var names []string
	if req.Operation == "patch" {
		paths, err := PatchPaths(req.Patch)
		if err != nil {
			return result, err
		}
		files := map[string][]byte{}
		for _, name := range paths {
			s, err := f.read(name)
			if err != nil {
				return result, err
			}
			before[name] = s
			if !s.Absent {
				files[name] = s.Content
			}
		}
		changes, err := PlanPatch(req.Patch, files)
		if err != nil {
			return result, err
		}
		for _, change := range changes {
			mode := before[change.Path].Mode
			if mode == 0 {
				mode = 0600
			}
			names = append(names, change.Path)
			after[change.Path] = snapshot{Content: change.Content, Mode: mode, Absent: change.Delete}
		}
	} else {
		s, err := f.read(req.Path)
		if err != nil {
			return result, err
		}
		if req.Operation == "read" {
			return FileResult{Content: s.Content, Revision: s.revision()}, nil
		}
		if req.Revision == "" {
			return result, ErrInvalid
		}
		if s.revision() != req.Revision {
			return result, ErrFileRevision
		}
		before[req.Path] = s
		names = []string{req.Path}
		mode := s.Mode
		if s.Absent {
			mode = 0600
		}
		switch req.Operation {
		case "write":
			after[req.Path] = snapshot{Content: req.Content, Mode: mode}
		case "delete":
			if s.Absent {
				return result, os.ErrNotExist
			}
			after[req.Path] = snapshot{Absent: true}
		case "restore":
			restored, err := f.load(req.Version, req.Path)
			if err != nil {
				return result, err
			}
			after[req.Path] = restored
		default:
			return result, ErrInvalid
		}
	}
	// Retain every revision before touching a destination. A failure here cannot
	// delete a person's only copy, even if the version directory is on another FS.
	for _, name := range names {
		if len(after[name].Content) > MaxFileBytes {
			return FileResult{}, ErrFileSize
		}
		id, err := f.save(savedVersion{Path: name, Before: before[name], After: after[name]})
		if err != nil {
			return FileResult{}, err
		}
		result.Changes = append(result.Changes, FileReceipt{name, before[name].revision(), after[name].revision(), id})
	}
	mutated := false
	for _, name := range names {
		err := ctx.Err()
		if err == nil {
			err = f.replace(name, before[name], after[name])
		}
		if err != nil {
			if mutated || errors.Is(err, ErrUnknown) {
				return result, errors.Join(ErrUnknown, err)
			}
			return result, err
		}
		mutated = true
	}
	if len(names) == 1 {
		result.Revision = after[names[0]].revision()
	} else {
		data, _ := json.Marshal(result.Changes)
		sum := sha256.Sum256(data)
		result.Revision = hex.EncodeToString(sum[:])
	}
	return result, nil
}
func (f *Files) replace(name string, old, next snapshot) error {
	dir, base, err := f.parent(name, !next.Absent)
	if err != nil {
		return err
	}
	defer dir.Close()
	temp := ""
	if !next.Absent {
		temp, err = stage(dir, next.Content, next.Mode)
		if err != nil {
			return err
		}
		defer dir.Remove(temp)
	}
	current, err := readSnapshot(dir, base)
	if err != nil {
		return err
	}
	if current.revision() != old.revision() {
		return ErrFileRevision
	}
	if next.Absent {
		err = dir.Remove(base)
	} else if old.Absent {
		// Link publishes without clobbering a file created since the absence check.
		err = dir.Link(temp, base)
	} else {
		err = dir.Rename(temp, base)
	}
	if err != nil {
		return err
	}
	if err = syncDir(dir); err != nil {
		return errors.Join(ErrUnknown, err)
	}
	return nil
}
