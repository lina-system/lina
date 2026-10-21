// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"errors"
	"os"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Backup copies every recorded material revision, including recoverable trash,
// and verifies the copied bytes. The caller supplies an unpublished generation
// directory, quiesces other writers, snapshots canon and publishes the complete
// generation manifest last. On error it must discard the partial generation.
func (s *Store) Backup(ctx context.Context, destination string) ([]Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var manifest []Revision
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT asset_id,revision,path,hash,size FROM asset_revisions ORDER BY asset_id,revision")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r Revision
			if err := rows.Scan(&r.AssetID, &r.Revision, &r.Path, &r.Hash, &r.Size); err != nil {
				return err
			}
			if !validRevision(r) {
				return ErrInvalid
			}
			manifest = append(manifest, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	target, err := os.OpenRoot(destination)
	if err != nil {
		return nil, err
	}
	defer target.Close()
	for _, r := range manifest {
		if err := copyRevision(ctx, s.root, target, r); err != nil {
			return nil, err
		}
	}
	return manifest, ctx.Err()
}

func copyRevision(ctx context.Context, source, target *os.Root, r Revision) error {
	f, err := openRegular(source, r.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	hash, size, err := writeFile(ctx, target, r.Path, f)
	if err != nil {
		return err
	}
	if hash != r.Hash || size != r.Size {
		return errors.Join(ErrHashMismatch, removeFile(target, r.Path))
	}
	return verifyFile(ctx, target, r)
}

// VerifyBackup checks recorded hashes and sizes against an existing copy. It
// never derives expected hashes from that copy or trusts a path outside materials.
func VerifyBackup(ctx context.Context, path string, manifest []Revision) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, r := range manifest {
		if err := verifyFile(ctx, root, r); err != nil {
			return err
		}
	}
	return ctx.Err()
}
