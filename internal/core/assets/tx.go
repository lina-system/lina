// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const preparedDirectory = "staging/material-writes"

// MaterialWriter prepares new LINA materials for publication in another store's
// canon transaction. Core owns one writer per assets store and opens it before
// admission, while no prepared write from an earlier instance can still run.
// Opening reconciles durable markers left by interrupted writes.
type MaterialWriter struct {
	store *Store
	mu    sync.Mutex
}

func OpenMaterialWriter(ctx context.Context, store *Store) (*MaterialWriter, error) {
	if store == nil {
		return nil, ErrInvalid
	}
	w := &MaterialWriter{store: store}
	dir, err := store.root.Open(preparedDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !validID(entry.Name()) || !entry.Type().IsRegular() {
			return nil, ErrInvalid
		}
		if err := w.reconcile(ctx, entry.Name()); err != nil {
			return nil, err
		}
	}
	return w, nil
}

// PreparedMaterial owns immutable bytes but no retained asset metadata until
// Insert's enclosing transaction commits. Use it synchronously, call Insert at
// most once, and Close only after the enclosing canon.Write returns. Close is
// also required when the caller does not reach the transaction at all.
type PreparedMaterial struct {
	writer           *MaterialWriter
	asset            Asset
	inserted, closed bool
}

// Prepare syncs a recovery marker before writing bytes. Its serialization lock
// covers preparation only, allowing several materials in one canon transaction.
// Each prepared write owns distinct bytes and a recovery marker until Close.
// Neither writer nor Store locks span the enclosing canon transaction.
func (w *MaterialWriter) Prepare(ctx context.Context, body io.Reader, observedPath string, actor Actor) (*PreparedMaterial, error) {
	if body == nil || !validObservedPath(observedPath) || !actor.valid() {
		return nil, ErrInvalid
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	a := Asset{ID: newID(), Area: Lina, ObservedPath: observedPath, Revision: 1, Actor: actor}
	a.Path = revisionPath(a.Area, a.ID, a.Revision)
	marker := filepath.Join(preparedDirectory, a.ID)
	if _, _, err := writeFile(ctx, w.store.root, marker, strings.NewReader("")); err != nil {
		return nil, err
	}
	var err error
	a.Hash, a.Size, err = writeFile(ctx, w.store.root, a.Path, body)
	if err != nil {
		cleanup := w.finish(a.ID)
		return nil, errors.Join(err, cleanup)
	}
	return &PreparedMaterial{writer: w, asset: a}, nil
}

// Insert publishes metadata using the caller's fenced transaction, without
// opening a second write or doing file I/O. Its Asset is provisional until the
// entire transaction commits, together with the referring ledger event.
func (p *PreparedMaterial) Insert(ctx context.Context, tx *canon.Tx) (Asset, error) {
	if p == nil || p.closed || p.inserted || tx == nil {
		return Asset{}, ErrInvalid
	}
	p.inserted = true
	a := p.asset
	if _, err := tx.ExecContext(ctx, "INSERT INTO assets(id,area,observed_path,revision,person_id,agent_id) VALUES(?,?,?,?,?,?)", a.ID, a.Area, a.ObservedPath, a.Revision, a.Actor.PersonID, a.Actor.AgentID); err != nil {
		return Asset{}, err
	}
	if err := insertRevision(ctx, tx, a); err != nil {
		return Asset{}, err
	}
	return a, nil
}

// Close checks committed canon rather than trusting a caller-supplied outcome.
// Failed writes lose their prepared bytes; committed writes keep them. A
// cleanup failure leaves the marker for recovery and never revokes a commit.
func (p *PreparedMaterial) Close() error {
	if p == nil || p.closed {
		return nil
	}
	p.closed = true
	p.writer.mu.Lock()
	defer p.writer.mu.Unlock()
	return p.writer.finish(p.asset.ID)
}

func (w *MaterialWriter) finish(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.reconcile(ctx, id)
}

func (w *MaterialWriter) reconcile(ctx context.Context, id string) error {
	path := revisionPath(Lina, id, 1)
	var recorded string
	err := w.store.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT path FROM asset_revisions WHERE asset_id=? AND revision=1", id).Scan(&recorded)
	})
	if errors.Is(err, sql.ErrNoRows) {
		if err := removeFile(w.store.root, path); err != nil {
			return err
		}
		// These directories belong only to this freshly prepared asset. Remove
		// them only when empty; unexpected contents retain the recovery marker.
		for _, dir := range []string{filepath.Dir(path), filepath.Dir(filepath.Dir(path))} {
			if err := removeFile(w.store.root, dir); err != nil {
				return err
			}
		}
	} else if err != nil {
		return err
	} else if recorded != path {
		return ErrInvalid
	}
	return removeFile(w.store.root, filepath.Join(preparedDirectory, id))
}
