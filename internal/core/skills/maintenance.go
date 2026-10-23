// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// Maintenance is an instruction-only prune or merge of existing user skills.
// Target is retained by a merge; Sources leave the catalog. All members share
// one transaction and one undo handle. Reason is a one-line explanation.
type Maintenance struct {
	Kind    string `json:"kind"`
	Target  Undo   `json:"target"`
	Sources []Undo `json:"sources,omitempty"`
	Files   Bundle `json:"files,omitempty"`
	Reason  string `json:"reason"`
}
type MaintenanceNotice struct {
	Sequence int64 `json:"sequence"`
	Applied
}

// Builtin identifies the release-owned namespace, including local overlays.
func (s *Store) Builtin(id string) bool { _, ok := s.index[id]; return ok }

// MaintainTx shares the ordinary skill version writer. The caller commits the
// receipt, job watermark and these writes in the same fenced canon transaction.
func (s *Store) MaintainTx(ctx context.Context, tx *canon.Tx, c Maintenance) (Applied, error) {
	if !line(c.Reason) || len(c.Reason) > 512 || len(c.Sources) > 31 {
		return Applied{}, ErrInvalid
	}
	switch c.Kind {
	case "prune":
		if len(c.Sources) != 0 || len(c.Files) != 0 {
			return Applied{}, ErrInvalid
		}
	case "merge":
		if len(c.Sources) == 0 || !bounded(c.Files) {
			return Applied{}, ErrInvalid
		}
		if err := instructionOnly(c.Files); err != nil {
			return Applied{}, err
		}
		name, _, _ := metadata(c.Files["SKILL.md"].Text)
		if name != c.Target.ID {
			return Applied{}, ErrInvalid
		}
	default:
		return Applied{}, ErrInvalid
	}
	members := append([]Undo{c.Target}, c.Sources...)
	seen := map[string]bool{}
	// Fingerprint the input content and member set, not the proposed wording,
	// chosen destination, candidate ID or revisions minted by a previous undo.
	hashes := map[string]string{}
	for _, m := range members {
		if !validID(m.ID) || m.Revision <= 0 || seen[m.ID] || s.Builtin(m.ID) {
			return Applied{}, ErrInvalid
		}
		seen[m.ID] = true
		r, err := s.current(ctx, tx, m.ID)
		if err != nil {
			return Applied{}, err
		}
		if r.Revision != m.Revision || r.deleted {
			return Applied{}, ErrConflict
		}
		files, err := s.files(r)
		if err != nil {
			return Applied{}, err
		}
		if err = instructionOnly(files); err != nil {
			return Applied{}, err
		}
		hashes[m.ID] = r.hash
	}
	raw, _ := json.Marshal(struct {
		Kind    string
		Members map[string]string
	}{c.Kind, hashes})
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	var rejected int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM skill_maintenance WHERE operation_hash=? AND undone=1", key).Scan(&rejected); err != nil {
		return Applied{}, err
	}
	if rejected != 0 {
		return Applied{}, ErrUndone
	}
	// Check all resulting bundles before saving any immutable Git objects.
	for _, m := range members {
		files := Bundle{}
		if c.Kind == "merge" && m.ID == c.Target.ID {
			files = c.Files
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM skill_undone WHERE id=? AND content_hash=?", m.ID, digest(files)).Scan(&rejected); err != nil {
			return Applied{}, err
		}
		if rejected != 0 {
			return Applied{}, ErrUndone
		}
	}
	var out Applied
	for _, m := range members {
		files := Bundle{}
		if c.Kind == "merge" && m.ID == c.Target.ID {
			files = c.Files
		}
		a, err := s.changeTx(ctx, tx, m.ID, m.Revision, func(*canon.Tx, record) (Bundle, error) { return files, nil }, false, true)
		if err != nil {
			return Applied{}, err
		}
		if m.ID == c.Target.ID {
			out = a
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_maintenance_members VALUES(?,?,?,?)", c.Target.ID, c.Target.Revision+1, m.ID, a.Version.Revision); err != nil {
			return Applied{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO skill_maintenance(id,revision,operation_hash,kind) VALUES(?,?,?,?)", out.Version.ID, out.Version.Revision, key, c.Kind); err != nil {
		return Applied{}, err
	}
	action := "Pruned "
	if c.Kind == "merge" {
		action = "Merged skills into "
	}
	out.Maintenance = true
	out.Notice = action + c.Target.ID + ": " + c.Reason
	if err := maintenanceNotice(ctx, tx, out); err != nil {
		return Applied{}, err
	}
	return out, nil
}

func maintenanceNotice(ctx context.Context, tx *canon.Tx, a Applied) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO skill_maintenance_notices(receipt) VALUES(?)", raw)
	return err
}

// Undo on any member restores the entire operation, refusing if even one head
// has since changed. In particular it cannot overwrite subsequent user work.
func (s *Store) undoMaintenanceTx(ctx context.Context, tx *canon.Tx, u Undo) (Applied, bool, error) {
	var id string
	var rev int64
	err := tx.QueryRowContext(ctx, "SELECT operation_id,operation_revision FROM skill_maintenance_members WHERE id=? AND revision=?", u.ID, u.Revision).Scan(&id, &rev)
	if errors.Is(err, sql.ErrNoRows) {
		return Applied{}, false, nil
	}
	if err != nil {
		return Applied{}, false, err
	}
	var undone bool
	if err = tx.QueryRowContext(ctx, "SELECT undone FROM skill_maintenance WHERE id=? AND revision=?", id, rev).Scan(&undone); err != nil {
		return Applied{}, true, err
	}
	if undone {
		return Applied{}, true, ErrConflict
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,revision FROM skill_maintenance_members WHERE operation_id=? AND operation_revision=? ORDER BY id", id, rev)
	if err != nil {
		return Applied{}, true, err
	}
	var members []Undo
	for rows.Next() {
		var m Undo
		if err = rows.Scan(&m.ID, &m.Revision); err != nil {
			break
		}
		members = append(members, m)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return Applied{}, true, err
	}
	prior := map[string]Bundle{}
	for _, m := range members {
		r, err := s.current(ctx, tx, m.ID)
		if err != nil {
			return Applied{}, true, err
		}
		if r.Revision != m.Revision {
			return Applied{}, true, ErrConflict
		}
		old, err := readRecord(ctx, tx, m.ID, m.Revision-1)
		if err != nil {
			return Applied{}, true, err
		}
		prior[m.ID], err = s.files(old)
		if err != nil {
			return Applied{}, true, err
		}
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM skill_undone WHERE id=? AND content_hash=?", m.ID, digest(prior[m.ID])).Scan(&n); err != nil {
			return Applied{}, true, err
		}
		if n != 0 {
			return Applied{}, true, ErrUndone
		}
		// A merge may retain an identical target. Rejecting that unchanged hash
		// would incorrectly reject the original procedure restored by this undo.
		if !r.deleted && digest(prior[m.ID]) != r.hash {
			if _, err = tx.ExecContext(ctx, "INSERT INTO skill_undone VALUES(?,?) ON CONFLICT DO NOTHING", m.ID, r.hash); err != nil {
				return Applied{}, true, err
			}
		}
	}
	var out Applied
	for _, m := range members {
		a, err := s.changeTx(ctx, tx, m.ID, m.Revision, func(*canon.Tx, record) (Bundle, error) { return prior[m.ID], nil }, true, false)
		if err != nil {
			return Applied{}, true, err
		}
		if m.ID == id {
			out = a
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE skill_maintenance SET undone=1 WHERE id=? AND revision=?", id, rev); err != nil {
		return Applied{}, true, err
	}
	out.Maintenance = true
	out.Notice = "Undid skill maintenance for " + id + "."
	return out, true, maintenanceNotice(ctx, tx, out)
}

func (s *Store) MaintenanceNotices(ctx context.Context) ([]MaintenanceNotice, error) {
	out := []MaintenanceNotice{}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT sequence,receipt FROM skill_maintenance_notices ORDER BY sequence")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n MaintenanceNotice
			var raw []byte
			if err = rows.Scan(&n.Sequence, &raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &n.Applied); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

// MaintenanceSnapshot loads only user-created skills for the background pass.
// Built-in IDs, including patched overlays, are never candidates.
func (s *Store) MaintenanceSnapshot(ctx context.Context) ([]Body, error) {
	catalog, err := s.Catalog(ctx)
	if err != nil {
		return nil, err
	}
	out := []Body{}
	for _, v := range catalog {
		if s.Builtin(v.ID) {
			continue
		}
		b, err := s.Read(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return strings.Compare(out[i].Version.ID, out[j].Version.ID) < 0 })
	return out, nil
}
