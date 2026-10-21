// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// SaveRunResult hands a run's result to LINA materials with a stable identity.
// The random run id doubles as the material id, making a crash between material
// publication and recording the landing recoverable. Only matching ownership
// and the reserved observed path can reuse that identity. Revisions are retained.
func (s *Store) SaveRunResult(ctx context.Context, id string, body []byte, actor Actor) (Asset, error) {
	if !validID(id) || !actor.valid() {
		return Asset{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.get(ctx, id, false)
	if err == nil {
		if a.Area != Lina || a.ObservedPath != "runs/"+id+".json" || a.Actor != actor {
			return Asset{}, ErrInvalid
		}
		return s.edit(ctx, id, a.Revision, bytes.NewReader(body), actor)
	}
	if !errors.Is(err, ErrNotFound) {
		return Asset{}, err
	}
	a = Asset{ID: id, Area: Lina, ObservedPath: "runs/" + id + ".json", Revision: 1, Actor: actor}
	a.Path = revisionPath(a.Area, id, a.Revision)
	// An interrupted publication may have left an unrecorded revision. Never
	// replace its bytes; retry in the next free revision, as Edit does.
	for {
		if err := ctx.Err(); err != nil {
			return Asset{}, err
		}
		if a.Revision <= 0 {
			return Asset{}, ErrInvalid
		}
		_, err := s.root.Lstat(a.Path)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return Asset{}, err
		}
		a.Revision++
		a.Path = revisionPath(a.Area, id, a.Revision)
	}
	a.Hash, a.Size, err = writeFile(ctx, s.root, a.Path, bytes.NewReader(body))
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
	return a, nil
}
