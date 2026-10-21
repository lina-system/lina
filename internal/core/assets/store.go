// SPDX-License-Identifier: Apache-2.0

// Package assets owns material bytes, revision metadata and import/export
// staging. Core owns one Store per state root and closes it after its users stop.
// Canon must be opened with canon.Migrations under Core's existing writer lock.
package assets

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrInvalid       = errors.New("invalid_asset")
	ErrNotFound      = errors.New("asset_not_found")
	ErrStaleRevision = errors.New("stale_revision")
	ErrTrashed       = errors.New("asset_trashed")
	ErrHashMismatch  = errors.New("asset_hash_mismatch")
)

type Area string

const (
	User Area = "user"
	Lina Area = "lina"
)

type Actor struct{ PersonID, AgentID string }

func (a Actor) valid() bool { return a.PersonID != "" && a.AgentID != "" }

type Asset struct {
	ID           string
	Area         Area
	ObservedPath string
	Revision     int64
	Path, Hash   string
	Size         int64
	Actor        Actor
	TrashedAt    int64 // Unix nanoseconds; zero means active.
}

// Revision is a backup manifest entry. Path is relative to the state root;
// ObservedPath is separate metadata and never used for filesystem access.
type Revision struct {
	AssetID    string
	Revision   int64
	Path, Hash string
	Size       int64
}

type Store struct {
	mu   sync.Mutex
	root *os.Root
	db   *canon.Store
}

func Open(ctx context.Context, path string, db *canon.Store) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, db: db}
	info, err := root.Stat(".")
	if err == nil && info.Mode().Perm() != 0700 {
		err = ErrInvalid
	}
	if err == nil {
		err = db.Read(ctx, func(tx *canon.Tx) error {
			var count int
			return tx.QueryRowContext(ctx, "SELECT count(*) FROM assets").Scan(&count)
		})
	}
	// A delivery interrupted by a crash remains unknown. Its temporary bytes
	// are disposable; reopening never repeats an external effect.
	if err == nil {
		err = s.cleanExports()
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.root.Close() }

// StageImport consumes an input stream without creating an asset or writing
// outside staging/import. Its opaque token can be handed over after a restart.
func (s *Store) StageImport(ctx context.Context, body io.Reader) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stage(ctx, body)
}

func (s *Store) stage(ctx context.Context, body io.Reader) (string, error) {
	token := newID()
	_, _, err := writeFile(ctx, s.root, filepath.Join("staging/import", token), body)
	if err != nil {
		return "", err
	}
	return token, nil
}

func validObservedPath(path string) bool { return path != "" && !strings.ContainsRune(path, 0) }

// Handover records ownership of a staged material. User is for imports; Lina
// is for produced binary materials. Version-space text has a different owner.
func (s *Store) Handover(ctx context.Context, token, observedPath string, area Area, actor Actor) (a Asset, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validID(token) || !validObservedPath(observedPath) || (area != User && area != Lina) || !actor.valid() {
		return a, ErrInvalid
	}
	staged := filepath.Join("staging/import", token)
	f, err := openRegular(s.root, staged)
	if err != nil {
		return a, err
	}
	defer f.Close()
	a = Asset{ID: newID(), Area: area, ObservedPath: observedPath, Revision: 1, Actor: actor}
	a.Path = revisionPath(area, a.ID, 1)
	a.Hash, a.Size, err = writeFile(ctx, s.root, a.Path, f)
	if err != nil {
		return Asset{}, err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO assets(id,area,observed_path,revision,person_id,agent_id) VALUES (?,?,?,?,?,?)", a.ID, a.Area, a.ObservedPath, a.Revision, actor.PersonID, actor.AgentID); err != nil {
			return err
		}
		return insertRevision(ctx, tx, a)
	})
	if err != nil {
		return Asset{}, errors.Join(err, removeFile(s.root, a.Path))
	}
	return a, removeFile(s.root, staged)
}

func insertRevision(ctx context.Context, tx *canon.Tx, a Asset) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO asset_revisions(asset_id,revision,path,hash,size,person_id,agent_id) VALUES (?,?,?,?,?,?,?)", a.ID, a.Revision, a.Path, a.Hash, a.Size, a.Actor.PersonID, a.Actor.AgentID)
	return err
}

const assetQuery = `SELECT a.id,a.area,a.observed_path,a.revision,r.path,r.hash,r.size,a.person_id,a.agent_id,a.trashed_at
 FROM assets a JOIN asset_revisions r ON r.asset_id=a.id AND r.revision=a.revision`

type scanner interface{ Scan(...any) error }

func scanAsset(row scanner) (a Asset, err error) {
	err = row.Scan(&a.ID, &a.Area, &a.ObservedPath, &a.Revision, &a.Path, &a.Hash, &a.Size, &a.Actor.PersonID, &a.Actor.AgentID, &a.TrashedAt)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func (s *Store) get(ctx context.Context, id string, trash bool) (a Asset, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		var err error
		a, err = scanAsset(tx.QueryRowContext(ctx, assetQuery+" WHERE a.id=?", id))
		return err
	})
	if err == nil && !trash && a.TrashedAt != 0 {
		err = ErrTrashed
	}
	if err != nil {
		return Asset{}, err
	}
	return a, nil
}

func (s *Store) Get(ctx context.Context, id string) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.get(ctx, id, false)
}

func (s *Store) Rename(ctx context.Context, id, observedPath string, actor Actor) (Asset, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validObservedPath(observedPath) || !actor.valid() {
		return Asset{}, ErrInvalid
	}
	a, err := s.get(ctx, id, false)
	if err != nil {
		return Asset{}, err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE assets SET observed_path=?,person_id=?,agent_id=? WHERE id=?", observedPath, actor.PersonID, actor.AgentID, id)
		return err
	})
	if err != nil {
		return Asset{}, err
	}
	a.ObservedPath, a.Actor = observedPath, actor
	return a, nil
}

func (s *Store) Edit(ctx context.Context, id string, base int64, body io.Reader, actor Actor) (a Asset, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !actor.valid() {
		return a, ErrInvalid
	}
	a, err = s.get(ctx, id, false)
	if err != nil {
		return a, err
	}
	if base != a.Revision {
		return Asset{}, ErrStaleRevision
	}
	token, err := s.stage(ctx, body)
	if err != nil {
		return Asset{}, err
	}
	staged := filepath.Join("staging/import", token)
	defer func() { err = errors.Join(err, removeFile(s.root, staged)) }()
	f, err := openRegular(s.root, staged)
	if err != nil {
		return Asset{}, err
	}
	defer f.Close()
	next := a
	// Never replace existing bytes: a successor may have committed them while
	// this caller read its input, or a crash may have left an unrecorded file.
	// Gaps are harmless; the canon fence and base revision decide publication.
	for {
		next.Revision++
		if next.Revision <= 0 {
			return Asset{}, ErrInvalid
		}
		next.Path = revisionPath(a.Area, id, next.Revision)
		if _, err := s.root.Lstat(next.Path); errors.Is(err, os.ErrNotExist) {
			break
		} else if err != nil {
			return Asset{}, err
		}
		if err := ctx.Err(); err != nil {
			return Asset{}, err
		}
	}
	next.Actor = actor
	next.Hash, next.Size, err = writeFile(ctx, s.root, next.Path, f)
	if err != nil {
		return Asset{}, err
	}
	if next.Hash == a.Hash {
		return a, removeFile(s.root, next.Path)
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		result, err := tx.ExecContext(ctx, "UPDATE assets SET revision=?,person_id=?,agent_id=? WHERE id=? AND revision=? AND trashed_at=0", next.Revision, actor.PersonID, actor.AgentID, id, base)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return ErrStaleRevision
		}
		return insertRevision(ctx, tx, next)
	})
	if err != nil {
		return Asset{}, errors.Join(err, removeFile(s.root, next.Path))
	}
	return next, nil
}

func (s *Store) revision(ctx context.Context, id string, rev int64) (r Revision, err error) {
	if _, err := s.get(ctx, id, false); err != nil {
		return r, err
	}
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT asset_id,revision,path,hash,size FROM asset_revisions WHERE asset_id=? AND revision=?", id, rev).Scan(&r.AssetID, &r.Revision, &r.Path, &r.Hash, &r.Size)
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil && !validRevision(r) {
		err = ErrInvalid
	}
	return
}

func (s *Store) Read(ctx context.Context, id string, rev int64) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.revision(ctx, id, rev)
	if err != nil {
		return nil, err
	}
	f, err := openRegular(s.root, r.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(contextReader{ctx, f})
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != r.Size || fmt.Sprintf("%x", sha256.Sum256(data)) != r.Hash {
		return nil, ErrHashMismatch
	}
	return data, ctx.Err()
}
