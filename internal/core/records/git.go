// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// go-git supplies object hashing, compression and index encoding. The local
// adapter builds the record tree and linear commit headers. Writes stay in the rooted,
// fsynced publication adapter; neither a subprocess nor transport is needed.
func objectID(kind string, data []byte) (string, []byte) {
	t, _ := plumbing.ParseObjectType(kind)
	return plumbing.ComputeHash(t, data).String(), data
}
func encodeObject(kind string, data []byte) ([]byte, error) {
	t, err := plumbing.ParseObjectType(kind)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	w := objfile.NewWriter(&b)
	if err = w.WriteHeader(t, int64(len(data))); err != nil {
		w.Close()
		return nil, err
	}
	_, err = w.Write(data)
	err = errors.Join(err, w.Close())
	return b.Bytes(), err
}
func decodeObject(kind string, raw []byte) ([]byte, error) {
	r, err := objfile.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	t, size, err := r.Header()
	if err != nil || t.String() != kind || size < 0 || size > 32<<20 {
		return nil, ErrRecordsChanged
	}
	data, err := io.ReadAll(io.LimitReader(r, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, ErrRecordsChanged
	}
	return data, nil
}
func (r *Store) tree(files map[string]string) (string, error) {
	type entry struct{ name, mode, hash string }
	var entries []entry
	dirs := map[string]map[string]string{}
	for name, hash := range files {
		dir, rest, nested := strings.Cut(name, "/")
		if !nested {
			entries = append(entries, entry{name, "100644", hash})
			continue
		}
		if dirs[dir] == nil {
			dirs[dir] = map[string]string{}
		}
		dirs[dir][rest] = hash
	}
	for dir, children := range dirs {
		hash, err := r.tree(children)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry{dir, "40000", hash})
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].name, entries[j].name
		if entries[i].mode == "40000" {
			a += "/"
		}
		if entries[j].mode == "40000" {
			b += "/"
		}
		return a < b
	})
	var data []byte
	for _, e := range entries {
		hash, _ := hex.DecodeString(e.hash)
		data = append(data, []byte(e.mode+" "+e.name+"\x00")...)
		data = append(data, hash...)
	}
	return r.object("tree", data)
}
func (r *Store) readTree(hash, prefix string, files map[string]string, depth int) error {
	if depth > 32 {
		return ErrRecordsChanged
	}
	data, err := r.readObject(hash, "tree")
	if err != nil {
		return err
	}
	for len(data) > 0 {
		header, rest, ok := bytes.Cut(data, []byte{0})
		if !ok || len(rest) < 20 {
			return ErrRecordsChanged
		}
		mode, name, ok := strings.Cut(string(header), " ")
		if !ok || name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
			return ErrRecordsChanged
		}
		full := path.Join(prefix, name)
		id := hex.EncodeToString(rest[:20])
		data = rest[20:]
		switch mode {
		case "40000":
			if err := r.readTree(id, full, files, depth+1); err != nil {
				return err
			}
		case "100644":
			if _, ok := files[full]; ok {
				return ErrRecordsChanged
			}
			files[full] = id
		default:
			return ErrRecordsChanged
		}
	}
	return nil
}
func (s *Store) makeCommit(tree, parent string, a Actor) (string, error) {
	person, _ := json.Marshal(a.Person)
	agent, _ := json.Marshal(a.Agent)
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	text := "tree " + tree + "\n"
	if parent != "" {
		text += "parent " + parent + "\n"
	}
	text += "author LINA <lina@example.invalid> " + stamp + " +0000\ncommitter LINA <lina@example.invalid> " + stamp + " +0000\n\nSave record\nPerson: " + string(person) + "\nAgent: " + string(agent) + "\n"
	return s.object("commit", []byte(text))
}

type gitCommit struct{ parent string }

func (s *Store) commitFiles(hash string) (gitCommit, map[string]string, error) {
	data, err := s.readObject(hash, "commit")
	if err != nil {
		return gitCommit{}, nil, err
	}
	header, _, ok := strings.Cut(string(data), "\n\n")
	if !ok {
		return gitCommit{}, nil, ErrRecordsChanged
	}
	var c gitCommit
	tree := ""
	for _, line := range strings.Split(header, "\n") {
		if value, ok := strings.CutPrefix(line, "tree "); ok {
			if tree != "" {
				return c, nil, ErrRecordsChanged
			}
			tree = value
		}
		if value, ok := strings.CutPrefix(line, "parent "); ok {
			if c.parent != "" {
				return c, nil, ErrRecordsChanged
			}
			c.parent = value
		}
	}
	if tree == "" {
		return c, nil, ErrRecordsChanged
	}
	files := map[string]string{}
	err = s.readTree(tree, "", files, 0)
	return c, files, err
}
func gitIndex(files map[string]string) ([]byte, error) {
	idx := &index.Index{Version: 2}
	for name, hash := range files {
		idx.Entries = append(idx.Entries, &index.Entry{Name: name, Hash: plumbing.NewHash(hash), Mode: filemode.Regular})
	}
	var b bytes.Buffer
	err := index.NewEncoder(&b).Encode(idx)
	return b.Bytes(), err
}

// Revision belongs to the record, not to unrelated writes in the same repo.
func (s *Store) revision(ctx context.Context, head, name, blob string) (Rev, error) {
	rev := head
	for head != "" {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c, _, err := s.commitFiles(head)
		if err != nil {
			return "", err
		}
		if c.parent == "" {
			break
		}
		parent := c.parent
		_, files, err := s.commitFiles(parent)
		if err != nil {
			return "", err
		}
		if files[name] != blob {
			break
		}
		rev = parent
		head = parent
	}
	return rev, nil
}

type indexRow struct {
	id, path, blob, revision string
	doc                      document
}

// Walk history once for all pending revisions, rather than once per record.
func (s *Store) indexRows(ctx context.Context, head string) ([]indexRow, error) {
	if head == "" {
		return nil, nil
	}
	_, files, err := s.commitFiles(head)
	if err != nil {
		return nil, err
	}
	rows := []indexRow{}
	ids := map[string]bool{}
	pending := map[string]int{}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !validPath(name) {
			return nil, ErrRecordsChanged
		}
		data, err := recordFile(s.root, name)
		if err != nil {
			return nil, err
		}
		hash, _ := objectID("blob", data)
		if hash != files[name] {
			return nil, ErrRecordsChanged
		}
		id, err := recordID(data)
		if err != nil || ids[id] {
			return nil, ErrRecordsChanged
		}
		d, err := parseDocument(data)
		if err != nil {
			return nil, ErrRecordsChanged
		}
		ids[id] = true
		pending[name] = len(rows)
		rows = append(rows, indexRow{id: id, path: name, blob: hash, doc: d})
	}
	for current := head; len(pending) > 0; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c, _, err := s.commitFiles(current)
		if err != nil {
			return nil, err
		}
		before := map[string]string{}
		if c.parent != "" {
			_, before, err = s.commitFiles(c.parent)
			if err != nil {
				return nil, err
			}
		}
		for name, i := range pending {
			if before[name] != rows[i].blob {
				rows[i].revision = current
				delete(pending, name)
			}
		}
		current = c.parent
	}
	return rows, nil
}
func replaceIndex(ctx context.Context, tx *canon.Tx, rows []indexRow) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM records_index"); err != nil {
		return err
	}
	// FTS is disposable too: clear orphaned rows after an interrupted restore.
	if _, err := tx.ExecContext(ctx, "DELETE FROM records_fts"); err != nil {
		return err
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, "INSERT INTO records_index(id,path,blob,revision) VALUES(?,?,?,?)", r.id, r.path, r.blob, r.revision); err != nil {
			return err
		}
		if err := putProjection(ctx, tx, r); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) Rebuild(ctx context.Context) error {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	head, _, err := s.snapshot(ctx)
	if err != nil {
		return err
	}
	return s.rebuild(ctx, head)
}

// Cached locators are derived; a missing or mismatching row falls back to Git.
func (s *Store) recordRevision(ctx context.Context, head string, r Record, blob string) (Rev, error) {
	var rev string
	err := s.db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT revision FROM records_index WHERE id=? AND path=? AND blob=?", r.ID, r.Path, blob).Scan(&rev)
	})
	if err == nil {
		return rev, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	return s.revision(ctx, head, r.Path, blob)
}
