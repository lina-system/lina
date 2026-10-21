// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // Git object identity, not an authorization primitive.
	"encoding/hex"
	"errors"
	"github.com/thisisjun786/lina/internal/core/canon"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func repositories(ctx context.Context, root *os.Root, snapshot string) ([]Repository, error) {
	published, err := publishedHeads(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	result := []Repository{}
	if _, err := root.Lstat("versions"); errors.Is(err, os.ErrNotExist) {
		if len(published) != 0 {
			return nil, ErrInvalid
		}
		return result, nil
	} else if err != nil {
		return nil, err
	}
	err = fs.WalkDir(root.FS(), "versions", func(path string, d fs.DirEntry, err error) error {
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
		head, canonical := published[path]
		if canonical {
			if err := verifyCommit(ctx, root, gitdir, head); err != nil {
				return err
			}
			delete(published, path)
		} else {
			var e error
			head, e = repositoryHead(root, gitdir)
			if e != nil {
				return e
			}
		}
		result = append(result, Repository{Path: path, Head: head})
		return fs.SkipDir
	})
	if err == nil && len(published) != 0 {
		err = ErrInvalid
	}
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

func publishedHeads(ctx context.Context, path string) (map[string]string, error) {
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	heads := map[string]string{}
	rows, err := db.QueryContext(ctx, "SELECT a.id,r.commit_id FROM agents a JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=a.revision")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, commit string
		if err = rows.Scan(&id, &commit); err != nil {
			rows.Close()
			return nil, err
		}
		heads["versions/agents/"+id] = commit
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	var exists int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE type='table' AND name='skill_repo'").Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 0 {
		rows, err = db.QueryContext(ctx, "SELECT commit_id FROM skill_repo WHERE singleton=1")
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var commit string
			if err = rows.Scan(&commit); err != nil {
				rows.Close()
				return nil, err
			}
			heads["versions/skills"] = commit
		}
		if err = errors.Join(rows.Err(), rows.Close()); err != nil {
			return nil, err
		}
	}
	return heads, nil
}

// Core retains published persona/skill commits as immutable loose Git objects.
func verifyCommit(ctx context.Context, root *os.Root, dir, head string) error {
	if len(head) != 40 {
		return ErrInvalid
	}
	if _, err := hex.DecodeString(head); err != nil || strings.ToLower(head) != head {
		return ErrInvalid
	}
	path := filepath.Join(dir, "objects", head[:2], head[2:])
	if err := regularPath(root, path); err != nil {
		return err
	}
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader, err := zlib.NewReader(f)
	if err != nil {
		return err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return ErrInvalid
	}
	digest := sha1.Sum(raw)
	if hex.EncodeToString(digest[:]) != head {
		return ErrHashMismatch
	}
	header, body, ok := bytes.Cut(raw, []byte{0})
	if !ok || string(header) != "commit "+strconv.Itoa(len(body)) {
		return ErrInvalid
	}
	return nil
}
