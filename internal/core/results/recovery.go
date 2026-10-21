// SPDX-License-Identifier: Apache-2.0
package results

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// A publication is journaled before changing any tracked file, index or ref.
// It names immutable Git objects, not a second copy of the document bytes.
type publication struct{ Parent, Commit, Path, Blob string }
type recordState struct {
	head, token string
	pending     *publication
}

func (r *Records) state(ctx context.Context) (state recordState, err error) {
	var raw []byte
	err = r.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT head,lock_token,pending FROM result_records_state WHERE singleton=1").Scan(&state.head, &state.token, &raw)
	})
	if err == nil && len(raw) != 0 {
		state.pending = new(publication)
		err = json.Unmarshal(raw, state.pending)
	}
	return
}
func (r *Records) acquire(ctx context.Context) (string, error) {
	token := newID()
	err := r.db.Write(ctx, func(tx *canon.Tx) error {
		changed, err := tx.ExecContext(ctx, "UPDATE result_records_state SET lock_token=? WHERE singleton=1 AND lock_token='' AND pending IS NULL", token)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRecordsChanged
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	// Linking a fully synced token avoids an empty lock with ambiguous ownership
	// if the process exits between exclusive creation and writing its contents.
	name := ".git/result-lock-" + token
	if err = r.atomic(name, []byte(token)); err != nil {
		return "", err
	}
	if err = r.root.Link(name, ".git/index.lock"); err != nil {
		return "", err
	}
	if err = syncDirectory(r.root, ".git"); err != nil {
		return "", err
	}
	return token, nil
}
func (r *Records) release(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	raw, err := regular(r.root, ".git/index.lock")
	if err == nil {
		if string(raw) != token {
			return ErrRecordsChanged
		}
		if err = r.root.Remove(".git/index.lock"); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = r.root.Remove(".git/result-lock-" + token); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = syncDirectory(r.root, ".git"); err != nil {
		return err
	}
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE result_records_state SET lock_token='' WHERE singleton=1 AND lock_token=? AND pending IS NULL", token)
		return err
	})
}
func (r *Records) remember(ctx context.Context, token string, p publication) error {
	raw, _ := json.Marshal(p)
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		changed, err := tx.ExecContext(ctx, "UPDATE result_records_state SET pending=? WHERE singleton=1 AND head=? AND lock_token=? AND pending IS NULL", raw, p.Parent, token)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRecordsChanged
		}
		return nil
	})
}
func (r *Records) checkpoint(ctx context.Context, p publication) error {
	raw, _ := json.Marshal(p)
	return r.db.Write(ctx, func(tx *canon.Tx) error {
		changed, err := tx.ExecContext(ctx, "UPDATE result_records_state SET head=?,pending=NULL WHERE singleton=1 AND head=? AND pending=?", p.Commit, p.Parent, raw)
		if err != nil {
			return err
		}
		n, err := changed.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrRecordsChanged
		}
		return nil
	})
}

// recover completes only the exact Core-journaled publication. An external
// commit, changed target bytes or foreign lock is refused without overwriting it.
// The result's effect remains unknown: recovery repairs repository consistency,
// never re-enters Save or clones another asset.
func (r *Records) recover(ctx context.Context) error {
	state, err := r.state(ctx)
	if err != nil {
		return err
	}
	if state.pending == nil {
		return r.release(ctx, state.token)
	}
	p := *state.pending
	if state.token == "" || p.Parent != state.head || !fs.ValidPath(p.Path) || p.Path == "." || strings.Split(p.Path, "/")[0] == ".git" {
		return ErrRecordsChanged
	}
	lock, err := regular(r.root, ".git/index.lock")
	if err != nil || string(lock) != state.token {
		return ErrRecordsChanged
	}
	current, _, err := r.head()
	if err != nil {
		return err
	}
	if current != p.Parent && current != p.Commit {
		return ErrRecordsChanged
	}
	commit, err := r.readObject(p.Commit, "commit")
	if err != nil {
		return err
	}
	line, rest, ok := strings.Cut(string(commit), "\n")
	if !ok || !strings.HasPrefix(line, "tree ") {
		return ErrRecordsChanged
	}
	if p.Parent != "" {
		if !strings.HasPrefix(rest, "parent "+p.Parent+"\n") {
			return ErrRecordsChanged
		}
	} else if strings.HasPrefix(rest, "parent ") {
		return ErrRecordsChanged
	}
	files := map[string]string{}
	if err = r.readTree(strings.TrimPrefix(line, "tree "), "", files, 0); err != nil {
		return err
	}
	if files[p.Path] != p.Blob {
		return ErrRecordsChanged
	}
	data, err := r.readObject(p.Blob, "blob")
	if err != nil {
		return err
	}
	// The working file may be absent (not installed yet) or exactly the intended
	// bytes. Every other file must match the intended tree.
	target, err := regular(r.root, p.Path)
	if err == nil {
		if !bytes.Equal(target, data) {
			return ErrRecordsChanged
		}
		if err = r.unchanged(files); err != nil {
			return err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		before := make(map[string]string, len(files))
		for name, hash := range files {
			if name != p.Path {
				before[name] = hash
			}
		}
		if err = r.unchanged(before); err != nil {
			return err
		}
	} else {
		return err
	}
	if err = r.atomic(p.Path, data); err != nil {
		return err
	}
	if err = r.atomic(".git/index", gitIndex(files)); err != nil {
		return err
	}
	if err = r.atomic(".git/refs/heads/records", []byte(p.Commit+"\n")); err != nil {
		return err
	}
	if err = r.checkpoint(ctx, p); err != nil {
		return err
	}
	return r.release(ctx, state.token)
}

// Atomic staging lives inside .git, so an interrupted stage never becomes an
// untracked worktree edit. Core's single writer excludes other active stages.
func (r *Records) cleanStages() error {
	entries, err := fs.ReadDir(r.root.FS(), ".git")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".result-stage-") && e.Type().IsRegular() {
			if err = r.root.Remove(path.Join(".git", e.Name())); err != nil {
				return err
			}
		}
	}
	return syncDirectory(r.root, ".git")
}
