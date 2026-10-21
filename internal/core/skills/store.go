// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/thisisjun786/lina/internal/core/canon"
)

const maxBundleBytes = 1 << 20

type Store struct {
	db        Canon
	root      *os.Root
	builtins  fs.FS
	index     map[string]Version
	writeFile func(*os.File, []byte) (int, error)
}

func Open(stateRoot string, db Canon, builtins fs.FS) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db, root: root, builtins: builtins, index: map[string]Version{}}
	if err = privateDirs(root, "versions/skills"); err == nil {
		err = recoverTemporaries(root)
	}
	if err == nil {
		err = s.immutable("versions/skills/config", []byte("[core]\nrepositoryformatversion = 0\nbare = true\n"))
	}
	if err == nil && builtins != nil {
		var entries []fs.DirEntry
		entries, err = fs.ReadDir(builtins, ".")
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				id := entry.Name()
				if !validID(id) {
					err = ErrInvalid
					break
				}
				var v Version
				v, err = builtinMetadata(builtins, id)
				if err != nil {
					break
				}
				s.index[id] = v
			}
		}
	}
	if err != nil {
		root.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.root.Close() }
func builtinMetadata(fsys fs.FS, id string) (Version, error) {
	f, err := fsys.Open(id + "/SKILL.md")
	if err != nil {
		return Version{}, err
	}
	defer f.Close()
	var header strings.Builder
	var one [1]byte
	var row strings.Builder
	lines := 0
	for {
		// Do not buffer ahead into a body: catalog construction reads exactly
		// the front matter, even for a built-in backed by a lazy filesystem.
		if header.Len() >= 16384 {
			return Version{}, ErrInvalid
		}
		if _, err := io.ReadFull(f, one[:]); err != nil {
			return Version{}, err
		}
		header.WriteByte(one[0])
		row.WriteByte(one[0])
		if one[0] != '\n' {
			continue
		}
		if lines > 0 && strings.TrimSpace(row.String()) == "---" {
			break
		}
		lines++
		row.Reset()
	}
	name, description, err := metadata(header.String() + "placeholder")
	return Version{ID: id, Name: name, Description: description, Commit: "builtin"}, err
}
func (s *Store) builtin(id string) (Bundle, error) {
	if _, ok := s.index[id]; !ok {
		return nil, ErrNotFound
	}
	files := Bundle{}
	err := fs.WalkDir(s.builtins, id, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return ErrAcceptance
		}
		if info.Size() > maxBundleBytes {
			return ErrInvalid
		}
		data, err := fs.ReadFile(s.builtins, path)
		if err != nil {
			return err
		}
		files[strings.TrimPrefix(path, id+"/")] = File{Text: string(data), Executable: info.Mode().Perm()&0111 != 0}
		return nil
	})
	return files, err
}

type record struct {
	Version
	hash    string
	deleted bool
}

func readRecord(ctx context.Context, tx *canon.Tx, id string, rev int64) (record, error) {
	r := record{Version: Version{ID: id, Revision: rev}}
	err := tx.QueryRowContext(ctx, "SELECT commit_id,name,description,content_hash,deleted FROM skill_versions WHERE id=? AND revision=?", id, rev).Scan(&r.Commit, &r.Name, &r.Description, &r.hash, &r.deleted)
	return r, err
}
func (s *Store) current(ctx context.Context, tx *canon.Tx, id string) (record, error) {
	var rev int64
	err := tx.QueryRowContext(ctx, "SELECT revision FROM skill_heads WHERE id=?", id).Scan(&rev)
	if errors.Is(err, sql.ErrNoRows) {
		if v, ok := s.index[id]; ok {
			return record{Version: v}, nil
		}
		return record{Version: Version{ID: id}, deleted: true}, nil
	}
	if err != nil {
		return record{}, err
	}
	return readRecord(ctx, tx, id, rev)
}

// Catalog reads only metadata. Callers retain this snapshot for the entire turn.
func (s *Store) Catalog(ctx context.Context) ([]Version, error) {
	entries := map[string]Version{}
	for id, v := range s.index {
		entries[id] = v
	}
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT v.id,v.revision,v.commit_id,v.name,v.description,v.deleted FROM skill_heads h JOIN skill_versions v ON v.id=h.id AND v.revision=h.revision`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v Version
			var deleted bool
			if err := rows.Scan(&v.ID, &v.Revision, &v.Commit, &v.Name, &v.Description, &deleted); err != nil {
				return err
			}
			if deleted {
				delete(entries, v.ID)
			} else {
				entries[v.ID] = v
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(entries))
	for _, v := range entries {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Store) Read(ctx context.Context, v Version) (Body, error) {
	if !validID(v.ID) {
		return Body{}, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return Body{}, err
	}
	var files Bundle
	var err error
	if v.Commit == "builtin" && v.Revision == 0 {
		if s.index[v.ID] != v {
			return Body{}, ErrNotFound
		}
		files, err = s.builtin(v.ID)
	} else {
		var r record
		err = s.db.Read(ctx, func(tx *canon.Tx) error { var err error; r, err = readRecord(ctx, tx, v.ID, v.Revision); return err })
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		if err == nil && (r.deleted || r.Version != v) {
			err = ErrNotFound
		}
		if err == nil {
			files, err = s.load(v.Commit, v.ID)
		}
		if err == nil && digest(files) != r.hash {
			err = ErrCorrupt
		}
	}
	if err != nil {
		return Body{}, err
	}
	return Body{Version: v, Files: files}, nil
}
func digest(files Bundle) string {
	data, _ := json.Marshal(files)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}
func bounded(files Bundle) bool {
	n := 0
	if len(files) > 128 {
		return false
	}
	for path, file := range files {
		n += len(path) + len(file.Text)
		if strings.Count(path, "/") > 16 {
			return false
		}
	}
	return n <= maxBundleBytes
}
func put(ctx context.Context, tx *canon.Tx, r record) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO skill_versions VALUES(?,?,?,?,?,?,?)", r.ID, r.Revision, r.Commit, r.Name, r.Description, r.hash, r.deleted)
	return err
}
func (s *Store) files(r record) (Bundle, error) {
	if r.deleted {
		return Bundle{}, nil
	}
	if r.Commit == "builtin" {
		return s.builtin(r.ID)
	}
	files, err := s.load(r.Commit, r.ID)
	if err == nil && digest(files) != r.hash {
		err = ErrCorrupt
	}
	return files, err
}
func (s *Store) Patch(ctx context.Context, p Patch) (Applied, error) {
	if !validID(p.ID) || p.Expected < 0 || len(p.Files) == 0 || !bounded(p.Files) {
		return Applied{}, ErrInvalid
	}
	return s.change(ctx, p.ID, p.Expected, func(tx *canon.Tx, old record) (Bundle, error) {
		files, err := s.files(old)
		if err != nil {
			return nil, err
		}
		// Classify the whole existing skill too: editing documentation must not
		// silently adopt an installed executable skill through the automatic path.
		if len(files) > 0 {
			if err = instructionOnly(files); err != nil {
				return nil, err
			}
		}
		for path, file := range p.Files {
			files[path] = file
		}
		if !bounded(files) {
			return nil, ErrInvalid
		}
		if err = instructionOnly(files); err != nil {
			return nil, err
		}
		var n int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM skill_undone WHERE id=? AND content_hash=?", p.ID, digest(files)).Scan(&n); err != nil {
			return nil, err
		}
		if n != 0 {
			return nil, ErrUndone
		}
		return files, nil
	}, false)
}
func (s *Store) Undo(ctx context.Context, u Undo) (Applied, error) {
	if !validID(u.ID) || u.Revision <= 0 {
		return Applied{}, ErrInvalid
	}
	return s.change(ctx, u.ID, u.Revision, func(tx *canon.Tx, old record) (Bundle, error) {
		prior, err := readRecord(ctx, tx, u.ID, u.Revision-1)
		if err != nil {
			return nil, err
		}
		files, err := s.files(prior)
		if err != nil {
			return nil, err
		}
		var rejected int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM skill_undone WHERE id=? AND content_hash=?", u.ID, digest(files)).Scan(&rejected); err != nil {
			return nil, err
		}
		if rejected != 0 {
			return nil, ErrUndone
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_undone VALUES(?,?) ON CONFLICT DO NOTHING", u.ID, old.hash); err != nil {
			return nil, err
		}
		return files, nil
	}, true)
}
func (s *Store) change(ctx context.Context, id string, expected int64, build func(*canon.Tx, record) (Bundle, error), undo bool) (Applied, error) {
	var out Applied
	err := s.db.Write(ctx, func(tx *canon.Tx) error {
		old, err := s.current(ctx, tx, id)
		if err != nil {
			return err
		}
		if old.Revision != expected {
			return ErrConflict
		}
		files, err := build(tx, old)
		if err != nil {
			return err
		}
		hash := digest(files)
		if !undo && !old.deleted && hash == old.hash {
			return ErrConflict
		}
		var parent string
		err = tx.QueryRowContext(ctx, "SELECT commit_id FROM skill_repo WHERE singleton=1").Scan(&parent)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old.Revision == 0 {
			baseline, err := s.files(old)
			if err != nil {
				return err
			}
			old.Commit, err = s.save(ctx, parent, id, baseline)
			if err != nil {
				return err
			}
			old.hash = digest(baseline)
			if err = put(ctx, tx, old); err != nil {
				return err
			}
			parent = old.Commit
		}
		current := record{Version: Version{ID: id, Revision: old.Revision + 1}, hash: hash, deleted: len(files) == 0}
		if !current.deleted {
			current.Name, current.Description, err = metadata(files["SKILL.md"].Text)
			if err != nil {
				return err
			}
		}
		current.Commit, err = s.save(ctx, parent, id, files)
		if err != nil {
			return err
		}
		if err = put(ctx, tx, current); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_heads VALUES(?,?) ON CONFLICT(id) DO UPDATE SET revision=excluded.revision", id, current.Revision); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO skill_repo VALUES(1,?) ON CONFLICT(singleton) DO UPDATE SET commit_id=excluded.commit_id", current.Commit); err != nil {
			return err
		}
		name := current.Name
		if name == "" {
			name = old.Name
		}
		notice := "Learned a procedure in " + name + "."
		if undo {
			notice = "Undid the change to " + name + "."
		}
		out = Applied{Version: current.Version, Notice: notice, Undo: Undo{ID: id, Revision: current.Revision}}
		return nil
	})
	// Never return a receipt (including a notice or undo handle) before commit.
	if err != nil {
		return Applied{}, err
	}
	return out, nil
}
