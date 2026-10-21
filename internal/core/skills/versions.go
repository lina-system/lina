// SPDX-License-Identifier: Apache-2.0
package skills

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/rand"
	"crypto/sha1" // Git's object format, not an authorization primitive.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Immutable Git refs retain saved skill commits; canon selects the published
// repository head. A stale writer cannot replace another published version.
func privateDirs(root *os.Root, path string) error {
	parent := "."
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		name := filepath.Join(parent, part)
		if err := root.Mkdir(name, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(name)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return ErrInvalid
		}
		if err := syncDir(root, parent); err != nil {
			return err
		}
		parent = name
	}
	return nil
}
func syncDir(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

// Core's control-file lock excludes other live writers. This lock also keeps
// recovery in another Store opened in this process away from active temp files.
var versionFiles sync.RWMutex

const temporaryPrefix = ".skill-tmp-"

func existingImmutable(root *os.Root, path string, data []byte) (bool, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if !info.Mode().IsRegular() {
		return true, ErrInvalid
	}
	old, err := root.ReadFile(path)
	if err != nil {
		return true, err
	}
	if !bytes.Equal(old, data) {
		return true, ErrCorrupt
	}
	return true, nil
}

func (s *Store) immutable(path string, data []byte) (err error) {
	versionFiles.RLock()
	defer versionFiles.RUnlock()
	root := s.root
	dir := filepath.Dir(path)
	if err := privateDirs(root, dir); err != nil {
		return err
	}
	if exists, err := existingImmutable(root, path, data); exists {
		if err != nil {
			return err
		}
		return syncDir(root, dir)
	}
	temporary := filepath.Join(dir, temporaryPrefix+newID())
	f, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		removed := root.Remove(temporary)
		if errors.Is(removed, os.ErrNotExist) {
			removed = nil
		}
		err = errors.Join(err, removed, syncDir(root, dir))
	}()
	write := s.writeFile
	if write == nil {
		write = (*os.File).Write
	}
	n, err := write(f, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	// Link installs complete, synced bytes atomically and never replaces a winner.
	if err := root.Link(temporary, path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		exists, err := existingImmutable(root, path, data)
		if err != nil {
			return err
		}
		if !exists {
			return os.ErrNotExist
		}
	}
	return syncDir(root, dir)
}

func recoverTemporaries(root *os.Root) error {
	versionFiles.Lock()
	defer versionFiles.Unlock()
	directories := make(map[string]bool)
	err := fs.WalkDir(root.FS(), "versions/skills", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		suffix, temporary := strings.CutPrefix(entry.Name(), temporaryPrefix)
		if !temporary || !validID(suffix) || !entry.Type().IsRegular() {
			return nil
		}
		if err := root.Remove(path); err != nil {
			return err
		}
		directories[filepath.Dir(path)] = true
		return nil
	})
	if err != nil {
		return err
	}
	for dir := range directories {
		if err := syncDir(root, dir); err != nil {
			return err
		}
	}
	return nil
}

func newID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func (s *Store) object(kind string, data []byte) (string, error) {
	content := append([]byte(fmt.Sprintf("%s %d\x00", kind, len(data))), data...)
	sum := sha1.Sum(content)
	hash := hex.EncodeToString(sum[:])
	var compressed bytes.Buffer
	w := zlib.NewWriter(&compressed)
	if _, err := w.Write(content); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	return hash, s.immutable(filepath.Join("versions/skills/objects", hash[:2], hash[2:]), compressed.Bytes())
}
func (s *Store) readObject(hash, kind string) ([]byte, error) {
	b, err := hex.DecodeString(hash)
	if err != nil || len(b) != 20 || hex.EncodeToString(b) != hash {
		return nil, ErrCorrupt
	}
	path := filepath.Join("versions/skills/objects", hash[:2], hash[2:])
	info, err := s.root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrCorrupt
	}
	f, err := s.root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := zlib.NewReader(f)
	if err != nil {
		return nil, ErrCorrupt
	}
	defer r.Close()
	content, err := io.ReadAll(io.LimitReader(r, maxBundleBytes+4096))
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(content)
	header, data, ok := bytes.Cut(content, []byte{0})
	if !ok || hex.EncodeToString(sum[:]) != hash || string(header) != kind+" "+strconv.Itoa(len(data)) {
		return nil, ErrCorrupt
	}
	return data, nil
}

type treeEntry struct{ mode, name, hash string }

func (s *Store) tree(entries []treeEntry) (string, error) {
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
	var body []byte
	for _, e := range entries {
		hash, err := hex.DecodeString(e.hash)
		if err != nil || len(hash) != 20 {
			return "", ErrCorrupt
		}
		body = append(body, []byte(e.mode+" "+e.name+"\x00")...)
		body = append(body, hash...)
	}
	return s.object("tree", body)
}
func (s *Store) entries(hash string) ([]treeEntry, error) {
	data, err := s.readObject(hash, "tree")
	if err != nil {
		return nil, err
	}
	var entries []treeEntry
	for len(data) > 0 {
		header, rest, ok := bytes.Cut(data, []byte{0})
		if !ok || len(rest) < 20 {
			return nil, ErrCorrupt
		}
		mode, name, ok := strings.Cut(string(header), " ")
		if !ok || !fs.ValidPath(name) || strings.Contains(name, "/") {
			return nil, ErrCorrupt
		}
		entries = append(entries, treeEntry{mode, name, hex.EncodeToString(rest[:20])})
		data = rest[20:]
	}
	return entries, nil
}
func (s *Store) commitTree(commit string) (string, error) {
	data, err := s.readObject(commit, "commit")
	if err != nil {
		return "", err
	}
	line, _, ok := strings.Cut(string(data), "\n")
	if !ok || !strings.HasPrefix(line, "tree ") {
		return "", ErrCorrupt
	}
	return strings.TrimPrefix(line, "tree "), nil
}
func (s *Store) bundleTree(files Bundle) (string, error) {
	children := map[string]Bundle{}
	var entries []treeEntry
	for path, file := range files {
		name, rest, nested := strings.Cut(path, "/")
		if nested {
			if children[name] == nil {
				children[name] = Bundle{}
			}
			children[name][rest] = file
			continue
		}
		hash, err := s.object("blob", []byte(file.Text))
		if err != nil {
			return "", err
		}
		mode := "100644"
		if file.Executable {
			mode = "100755"
		}
		entries = append(entries, treeEntry{mode, name, hash})
	}
	for name, files := range children {
		hash, err := s.bundleTree(files)
		if err != nil {
			return "", err
		}
		entries = append(entries, treeEntry{"40000", name, hash})
	}
	return s.tree(entries)
}
func (s *Store) save(ctx context.Context, parent, id string, files Bundle) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var entries []treeEntry
	if parent != "" {
		tree, err := s.commitTree(parent)
		if err != nil {
			return "", err
		}
		entries, err = s.entries(tree)
		if err != nil {
			return "", err
		}
	}
	kept := entries[:0]
	for _, entry := range entries {
		if entry.name != id {
			kept = append(kept, entry)
		}
	}
	entries = kept
	if len(files) > 0 {
		hash, err := s.bundleTree(files)
		if err != nil {
			return "", err
		}
		entries = append(entries, treeEntry{"40000", id, hash})
	}
	tree, err := s.tree(entries)
	if err != nil {
		return "", err
	}
	text := "tree " + tree + "\n"
	if parent != "" {
		text += "parent " + parent + "\n"
	}
	text += "author LINA <lina@example.invalid> 0 +0000\ncommitter LINA <lina@example.invalid> 0 +0000\n\nSkill revision: " + id + "\n"
	commit, err := s.object("commit", []byte(text))
	if err != nil {
		return "", err
	}
	if err = s.immutable("versions/skills/refs/revisions/"+commit, []byte(commit+"\n")); err != nil {
		return "", err
	}
	// HEAD anchors the first revision; canon chooses the current published head.
	if parent == "" {
		if err = s.immutable("versions/skills/HEAD", []byte(commit+"\n")); err != nil {
			// A prior uncommitted attempt may already have installed a valid anchor.
			if !errors.Is(err, ErrCorrupt) {
				return "", err
			}
		}
	}
	return commit, ctx.Err()
}
func (s *Store) load(commit, id string) (Bundle, error) {
	tree, err := s.commitTree(commit)
	if err != nil {
		return nil, err
	}
	entries, err := s.entries(tree)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.name == id && entry.mode == "40000" {
			files := Bundle{}
			err = s.loadTree(entry.hash, "", files, 0)
			return files, err
		}
	}
	return nil, ErrNotFound
}
func (s *Store) loadTree(hash, prefix string, files Bundle, depth int) error {
	if depth > 16 || len(files) > 128 {
		return ErrCorrupt
	}
	entries, err := s.entries(hash)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := prefix + entry.name
		if entry.mode == "40000" {
			if err := s.loadTree(entry.hash, path+"/", files, depth+1); err != nil {
				return err
			}
			continue
		}
		if entry.mode != "100644" && entry.mode != "100755" {
			return ErrCorrupt
		}
		data, err := s.readObject(entry.hash, "blob")
		if err != nil {
			return err
		}
		files[path] = File{Text: string(data), Executable: entry.mode == "100755"}
	}
	return nil
}
