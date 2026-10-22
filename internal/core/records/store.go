// SPDX-License-Identifier: Apache-2.0
// Package records owns the shared, versioned Markdown repository.
package records

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"

	"github.com/thisisjun786/lina/internal/core/canon"
)

var (
	ErrUncertain      = errors.New("record_publication_unknown")
	ErrPathExists     = errors.New("record_path_exists")
	ErrStaleRevision  = errors.New("stale_revision")
	ErrRecordsChanged = errors.New("records_changed")
	ErrNotFound       = errors.New("record_not_found")
	ErrInvalid        = errors.New("invalid_record")
)

type ID = string
type Rev = string
type CommitID = string
type Record struct {
	ID   ID
	Path string
	Data []byte
}
type Change struct {
	ID   ID
	Base Rev
	Path string
	Data []byte
}
type Actor struct{ Person, Agent string }
type Repo interface {
	Read(context.Context, ID) (Record, Rev, error)
	Commit(context.Context, Change, Actor) (Rev, error)
	Head(context.Context) (CommitID, error)
	Rebuild(context.Context) error
}
type Canon interface {
	Read(context.Context, func(*canon.Tx) error) error
	Write(context.Context, func(*canon.Tx) error) error
}
type Store struct {
	root *os.Root
	db   Canon
	// PublicationHook is a fault-injection seam for deterministic crash tests.
	// Production leaves it nil. An error after the intent is durably recoverable.
	PublicationHook func(string) error
}

var recordsMu sync.Mutex // Includes every handle, including the result saver.
var _ Repo = (*Store)(nil)

func Open(stateRoot string, db Canon) (*Store, error) {
	if db == nil {
		return nil, ErrInvalid
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = privateDirectories(root, "versions/records"); err != nil {
		return nil, err
	}
	repo, err := root.OpenRoot("versions/records")
	if err != nil {
		return nil, err
	}
	s := &Store{root: repo, db: db}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if err = s.initialize(); err == nil {
		err = s.recover(context.Background())
	}
	if err == nil {
		err = s.cleanStages()
	}
	if err != nil {
		repo.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.root.Close() }
func NewID() ID               { return newID() }
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func recordID(data []byte) (ID, error) {
	if len(data) > 2<<20 || !bytes.HasPrefix(data, []byte("---\n")) {
		return "", ErrInvalid
	}
	front, _, ok := bytes.Cut(data[4:], []byte("\n---\n"))
	if !ok {
		return "", ErrInvalid
	}
	var id string
	for _, line := range bytes.Split(front, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("id: ")) {
			if id != "" || json.Unmarshal(line[4:], &id) != nil || id == "" {
				return "", ErrInvalid
			}
		}
	}
	if id == "" {
		return "", ErrInvalid
	}
	return id, nil
}
func validPath(name string) bool {
	if !fs.ValidPath(name) || !strings.HasSuffix(name, ".md") || strings.Contains(name, "\\") || len(name) > 1024 {
		return false
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return false
	}
	switch parts[0] {
	case "inbox", "projects", "areas", "resources", "archive":
	default:
		return false
	}
	for _, p := range parts {
		if strings.HasPrefix(p, ".") {
			return false
		}
	}
	return true
}
func (s *Store) snapshot(ctx context.Context) (string, map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if err := s.recover(ctx); err != nil {
		return "", nil, err
	}
	head, files, err := s.head()
	if err != nil {
		return "", nil, err
	}
	state, err := s.state(ctx)
	if err != nil {
		return "", nil, err
	}
	// Outside commits require explicit reconciliation; no silent merge.
	if head != state.head {
		return "", nil, ErrRecordsChanged
	}
	if err = s.unchanged(files); err != nil {
		return "", nil, err
	}
	return head, files, nil
}
func (s *Store) find(files map[string]string, id ID) (Record, error) {
	found := Record{}
	ids := map[string]bool{}
	for name, hash := range files {
		if !validPath(name) {
			return Record{}, ErrRecordsChanged
		}
		data, err := s.readObject(hash, "blob")
		if err != nil {
			return Record{}, err
		}
		current, err := recordID(data)
		if err != nil || ids[current] {
			return Record{}, ErrRecordsChanged
		}
		ids[current] = true
		if current == id {
			found = Record{ID: current, Path: name, Data: data}
		}
	}
	if found.ID == "" {
		return Record{}, ErrNotFound
	}
	return found, nil
}
func (s *Store) Read(ctx context.Context, id ID) (Record, Rev, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	head, files, err := s.snapshot(ctx)
	if err != nil {
		return Record{}, "", err
	}
	r, err := s.find(files, id)
	if err != nil {
		return Record{}, "", err
	}
	rev, err := s.recordRevision(ctx, head, r, files[r.Path])
	return r, rev, err
}
func (s *Store) Head(ctx context.Context) (CommitID, error) {
	recordsMu.Lock()
	defer recordsMu.Unlock()
	head, _, err := s.snapshot(ctx)
	return head, err
}
func (s *Store) Commit(ctx context.Context, c Change, a Actor) (rev Rev, err error) {
	intent := false
	defer func() {
		if err != nil && intent {
			err = errors.Join(ErrUncertain, err)
		}
	}()
	recordsMu.Lock()
	defer recordsMu.Unlock()
	if a.Person == "" || a.Agent == "" || !validPath(c.Path) {
		return "", ErrInvalid
	}
	id, err := recordID(c.Data)
	if err != nil || id != c.ID {
		return "", ErrInvalid
	}
	parent, files, err := s.snapshot(ctx)
	if err != nil {
		return "", err
	}
	old, err := s.find(files, c.ID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if errors.Is(err, ErrNotFound) {
		if c.Base != "" {
			return "", ErrStaleRevision
		}
	} else {
		rev, err := s.recordRevision(ctx, parent, old, files[old.Path])
		if err != nil {
			return "", err
		}
		if c.Base == "" || c.Base != rev {
			return "", ErrStaleRevision
		}
		if old.Path == c.Path && bytes.Equal(old.Data, c.Data) {
			return rev, nil
		}
	}
	if files[c.Path] != "" && c.Path != old.Path {
		return "", ErrPathExists
	}
	token, err := s.acquire(ctx)
	if err != nil {
		return "", err
	}
	if err = s.publicationStep("lock"); err != nil {
		return "", err
	}
	blob, err := s.object("blob", c.Data)
	if err != nil {
		return "", err
	}
	oldBlob := files[old.Path]
	delete(files, old.Path)
	files[c.Path] = blob
	tree, err := s.tree(files)
	if err != nil {
		return "", err
	}
	commit, err := s.makeCommit(tree, parent, a)
	if err != nil {
		return "", err
	}
	p := publication{Parent: parent, Commit: commit, Path: c.Path, Blob: blob, OldPath: old.Path, OldBlob: oldBlob}
	intent = true
	if err = s.remember(ctx, token, p); err != nil {
		return "", err
	}
	if err = s.publicationStep("intent"); err != nil {
		return "", err
	}
	if err = s.install(p, files); err != nil {
		return "", err
	}
	if err = s.checkpoint(ctx, p); err != nil {
		return "", err
	}
	if err = s.publicationStep("checkpoint"); err != nil {
		return "", err
	}
	if err = s.release(ctx, token); err != nil {
		return "", err
	}
	return commit, nil
}
func (s *Store) publicationStep(stage string) error {
	if s.PublicationHook != nil {
		return s.PublicationHook(stage)
	}
	return nil
}
func (s *Store) install(p publication, files map[string]string) error {
	data, err := s.readObject(p.Blob, "blob")
	if err != nil {
		return err
	}
	if err = s.atomic(p.Path, data); err != nil {
		return err
	}
	if err = s.publicationStep("file"); err != nil {
		return err
	}
	if p.OldPath != "" && p.OldPath != p.Path {
		if err = s.root.Remove(p.OldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = syncDirectory(s.root, path.Dir(p.OldPath)); err != nil {
			return err
		}
	}
	if err = s.publicationStep("remove"); err != nil {
		return err
	}
	index, err := gitIndex(files)
	if err != nil {
		return err
	}
	if err = s.atomic(".git/index", index); err != nil {
		return err
	}
	if err = s.publicationStep("index"); err != nil {
		return err
	}
	if err = s.atomic(".git/refs/heads/records", []byte(p.Commit+"\n")); err != nil {
		return err
	}
	return s.publicationStep("ref")
}
