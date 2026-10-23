// SPDX-License-Identifier: Apache-2.0
package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

type FileStamp struct {
	Revision string `json:"revision"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

func (f *Files) isRetainedCaptureEntry(name string, entry fs.DirEntry, captured map[string]bool) bool {
	slotDir := ""
	switch {
	case entry.IsDir() && strings.HasPrefix(path.Base(name), recoveryPrefix):
		slotDir = name
	case entry.Type().IsRegular() && path.Base(name) == "file" && strings.HasPrefix(path.Base(path.Dir(name)), recoveryPrefix):
		slotDir = path.Dir(name)
	default:
		return false
	}
	if ok, seen := captured[slotDir]; seen {
		return ok
	}
	info, err := f.versions.Lstat(recoveryMarker(slotDir))
	ok := err == nil && info.Mode().IsRegular()
	captured[slotDir] = ok
	return ok
}

// manifest is an executor-only operation, not a model tool. Hashes are captured
// inside the sandbox; the shell's receipt can describe changed paths even when
// its exit status is a failure. Symlinks and protected Git metadata are excluded.
func (f *Files) manifest(ctx context.Context) (FileResult, error) {
	result := FileResult{Manifest: map[string]FileStamp{}}
	entries := 0
	captured := map[string]bool{}
	err := fs.WalkDir(f.root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !f.isRetainedCaptureEntry(name, entry, captured) {
			entries++
			if entries > 10000 {
				return ErrFileSize
			}
		}
		if path.Base(name) == ".git" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() || !entry.Type().IsRegular() {
			return nil
		}
		dir, base, err := f.parent(name, false)
		if err != nil {
			return err
		}
		defer dir.Close()
		file, err := openFileNoFollow(dir, base)
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrFileScope
		}
		h := sha256.New()
		fmt.Fprintf(h, "%o\x00", info.Mode().Perm())
		if _, err := io.Copy(h, file); err != nil {
			return err
		}
		result.Manifest[name] = FileStamp{hex.EncodeToString(h.Sum(nil)), info.Size(), info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		return FileResult{}, err
	}
	// Content/mode revisions determine the aggregate; a mere mtime change does
	// not make a different content revision, but remains available for auditing.
	revisions := map[string]string{}
	for name, stamp := range result.Manifest {
		revisions[name] = stamp.Revision
	}
	data, _ := json.Marshal(revisions)
	sum := sha256.Sum256(data)
	result.Revision = hex.EncodeToString(sum[:])
	return result, nil
}

func ManifestDiff(before, after map[string]FileStamp) []FileReceipt {
	paths := map[string]bool{}
	for name := range before {
		paths[name] = true
	}
	for name := range after {
		paths[name] = true
	}
	var names []string
	for name := range paths {
		names = append(names, name)
	}
	slices.Sort(names)
	var changes []FileReceipt
	for _, name := range names {
		a, b := AbsentRevision, AbsentRevision
		if stamp, ok := before[name]; ok {
			a = stamp.Revision
		}
		if stamp, ok := after[name]; ok {
			b = stamp.Revision
		}
		if a != b {
			changes = append(changes, FileReceipt{Path: name, Before: a, After: b})
		}
	}
	return changes
}
