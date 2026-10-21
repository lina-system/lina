// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/thisisjun786/lina/internal/core/canon"
)

type DeliveryState string

const (
	Unknown   DeliveryState = "unknown"
	Delivered DeliveryState = "delivered"
)

type ExportRecord struct {
	ID, AssetID       string
	Revision          int64
	Hash, Destination string
	Actor             Actor
	State             DeliveryState
}

// Export delivers only from export staging. The caller must check the grant
// for the person's destination before calling, and its send callback must finish
// delivery synchronously. No canon transaction or store lock spans the callback.
// A failed/interrupted delivery stays unknown and is never automatically retried.
func (s *Store) Export(ctx context.Context, id string, rev int64, destination string, actor Actor, send func(context.Context, io.Reader) error) (record ExportRecord, err error) {
	if destination == "" || !actor.valid() || send == nil {
		return record, ErrInvalid
	}
	s.mu.Lock()
	record, staged, err := s.prepareExport(ctx, id, rev, destination, actor)
	s.mu.Unlock()
	if err != nil {
		return record, err
	}
	defer func() { err = errors.Join(err, removeFile(s.root, staged)) }()
	f, err := openRegular(s.root, staged)
	if err != nil {
		return record, err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if err := send(ctx, contextReader{ctx, f}); err != nil {
		return record, err
	}
	err = s.db.Write(ctx, func(tx *canon.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE asset_exports SET state='delivered' WHERE id=?", record.ID)
		return err
	})
	if err == nil {
		record.State = Delivered
	}
	return record, err
}

func (s *Store) prepareExport(ctx context.Context, id string, rev int64, destination string, actor Actor) (record ExportRecord, staged string, err error) {
	r, err := s.revision(ctx, id, rev)
	if err != nil {
		return record, "", err
	}
	f, err := openRegular(s.root, r.Path)
	if err != nil {
		return record, "", err
	}
	defer f.Close()
	record = ExportRecord{ID: newID(), AssetID: id, Revision: rev, Hash: r.Hash, Destination: destination, Actor: actor, State: Unknown}
	staged = filepath.Join("staging/export", record.ID)
	hash, size, err := writeFile(ctx, s.root, staged, f)
	if err != nil {
		return ExportRecord{}, "", err
	}
	if hash != r.Hash || size != r.Size {
		err = ErrHashMismatch
	} else {
		err = s.db.Write(ctx, func(tx *canon.Tx) error {
			_, err := tx.ExecContext(ctx, "INSERT INTO asset_exports(id,asset_id,revision,hash,destination,person_id,agent_id,state) VALUES (?,?,?,?,?,?,?,?)", record.ID, id, rev, r.Hash, destination, actor.PersonID, actor.AgentID, Unknown)
			return err
		})
	}
	if err != nil {
		return ExportRecord{}, "", errors.Join(err, removeFile(s.root, staged))
	}
	return record, staged, nil
}

func (s *Store) Exports(ctx context.Context, id string) (records []ExportRecord, err error) {
	err = s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id,asset_id,revision,hash,destination,person_id,agent_id,state FROM asset_exports WHERE asset_id=? ORDER BY id", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ExportRecord
			if err := rows.Scan(&r.ID, &r.AssetID, &r.Revision, &r.Hash, &r.Destination, &r.Actor.PersonID, &r.Actor.AgentID, &r.State); err != nil {
				return err
			}
			records = append(records, r)
		}
		return rows.Err()
	})
	return
}

func (s *Store) cleanExports() error {
	dir, err := s.root.Open("staging/export")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !validID(entry.Name()) || !entry.Type().IsRegular() {
			return ErrInvalid
		}
		if err := removeFile(s.root, filepath.Join("staging/export", entry.Name())); err != nil {
			return err
		}
	}
	return nil
}
