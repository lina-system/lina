// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestEditRechecksAuthorityAfterReadingInput(t *testing.T) {
	for _, body := range []string{"original", "changed"} {
		for _, state := range []struct {
			name, sql string
			want      error
		}{
			{"epoch", "UPDATE canon_meta SET epoch='2'", canon.ErrStaleEpoch},
			{"revision", "UPDATE assets SET revision=2", assets.ErrStaleRevision},
			{"trash", "UPDATE assets SET trashed_at=1", assets.ErrTrashed},
		} {
			t.Run(body+"/"+state.name, func(t *testing.T) {
				s, _, root := setup(t)
				a := importAsset(t, s, "original")
				input := &callbackReader{Reader: strings.NewReader(body), before: func() {
					db, err := canon.OpenSQLite(t.Context(), filepath.Join(root, "canon.sqlite"))
					if err != nil {
						t.Fatal(err)
					}
					defer db.Close()
					if state.name == "revision" {
						// Publish a newer canon revision with equal content, so a
						// hash-only no-op check cannot establish current authority.
						if _, err := db.ExecContext(t.Context(), `INSERT INTO asset_revisions(asset_id,revision,path,hash,size,person_id,agent_id)
						 SELECT asset_id,2,replace(path,'/1/','/2/'),hash,size,person_id,agent_id FROM asset_revisions`); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := db.ExecContext(t.Context(), state.sql); err != nil {
						t.Fatal(err)
					}
				}}
				got, err := s.Edit(t.Context(), a.ID, a.Revision, input, actor)
				if !errors.Is(err, state.want) {
					t.Errorf("edit returned %+v, %v; want %v", got, err, state.want)
				}
				if got := files(t, filepath.Join(root, "staging/import")); len(got) != 0 {
					t.Errorf("staged leftovers: %v", got)
				}
				if got := files(t, filepath.Join(root, "materials")); len(got) != 1 {
					t.Errorf("unpublished revision leftovers: %v", got)
				}
			})
		}
	}
}
