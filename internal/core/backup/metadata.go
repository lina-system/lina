// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

func repositories(root *os.Root) ([]Repository, error) {
	result := []Repository{}
	if _, err := root.Lstat("versions"); errors.Is(err, os.ErrNotExist) {
		return result, nil
	} else if err != nil {
		return nil, err
	}
	err := fs.WalkDir(root.FS(), "versions", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		// Both Core's bare repositories and ordinary worktree repositories are kept.
		gitdir := path
		if info, e := root.Lstat(filepath.Join(path, ".git")); e == nil {
			if !info.IsDir() {
				return ErrInvalid
			}
			gitdir = filepath.Join(path, ".git")
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if _, e := root.Lstat(filepath.Join(gitdir, "objects")); errors.Is(e, os.ErrNotExist) {
			return nil
		} else if e != nil {
			return e
		}
		if _, e := root.Lstat(filepath.Join(gitdir, "HEAD")); e != nil {
			return e
		}
		head, e := repositoryHead(root, gitdir)
		if e != nil {
			return e
		}
		result = append(result, Repository{Path: path, Head: head})
		return fs.SkipDir
	})
	return result, err
}
func repositoryHead(root *os.Root, dir string) (string, error) {
	read := func(path string) (string, error) {
		if err := regularPath(root, path); err != nil {
			return "", err
		}
		data, err := root.ReadFile(path)
		return strings.TrimSpace(string(data)), err
	}
	head, err := read(filepath.Join(dir, "HEAD"))
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for strings.HasPrefix(head, "ref: ") {
		ref := strings.TrimPrefix(head, "ref: ")
		if !strings.HasPrefix(ref, "refs/") || !fs.ValidPath(ref) || seen[ref] {
			return "", ErrInvalid
		}
		seen[ref] = true
		head, err = read(filepath.Join(dir, ref))
		if errors.Is(err, os.ErrNotExist) {
			packed, e := read(filepath.Join(dir, "packed-refs"))
			if e != nil {
				return "", e
			}
			head = ""
			for _, line := range strings.Split(packed, "\n") {
				parts := strings.Fields(line)
				if len(parts) == 2 && parts[1] == ref {
					head = parts[0]
					break
				}
			}
			if head == "" {
				return "", ErrInvalid
			}
		} else if err != nil {
			return "", err
		}
	}
	if len(head) != 40 && len(head) != 64 {
		return "", ErrInvalid
	}
	if _, err := hex.DecodeString(head); err != nil || strings.ToLower(head) != head {
		return "", ErrInvalid
	}
	return head, nil
}
func derivedRevisions(root *os.Root) ([]Derived, error) {
	result := []Derived{}
	assets, err := fs.ReadDir(root.FS(), "derived")
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, asset := range assets {
		if !asset.IsDir() {
			return nil, ErrInvalid
		}
		base := filepath.Join("derived", asset.Name())
		revisions, err := fs.ReadDir(root.FS(), base)
		if err != nil {
			return nil, err
		}
		for _, revision := range revisions {
			if !revision.IsDir() {
				return nil, ErrInvalid
			}
			result = append(result, Derived{Path: filepath.Join(base, revision.Name()), AssetID: asset.Name(), SourceRevision: revision.Name()})
		}
	}
	return result, nil
}
