// SPDX-License-Identifier: Apache-2.0
package agents

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" // Git's object format, not an authorization primitive.
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// Each agent owns a bare Git repository. Immutable refs retain saved commits;
// canon selects the published revision. No mutable branch pointer lets a stale
// writer replace the live persona. HEAD anchors the initial revision.
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

const temporaryPrefix = ".persona-tmp-"

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
	err := fs.WalkDir(root.FS(), "versions/agents", func(path string, entry fs.DirEntry, err error) error {
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
func repoPath(id string) string { return filepath.Join("versions", "agents", id) }
func (s *Store) object(id, kind string, data []byte) (string, error) {
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
	err := s.immutable(filepath.Join(repoPath(id), "objects", hash[:2], hash[2:]), compressed.Bytes())
	return hash, err
}

type personaVersion struct {
	Layers
	WorkMethod json.RawMessage `json:"work_method,omitempty"`
}

func (s *Store) save(ctx context.Context, id, parent string, layers Layers, person string, method ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validID(id) {
		return "", ErrInvalid
	}
	dir := repoPath(id)
	if err := s.immutable(filepath.Join(dir, "config"), []byte("[core]\nrepositoryformatversion = 0\nbare = true\n")); err != nil {
		return "", err
	}
	version := personaVersion{Layers: layers}
	if len(method) > 0 {
		version.WorkMethod = json.RawMessage(method[0])
	} else if parent != "" {
		old, err := s.loadVersion(id, parent)
		if err != nil {
			return "", err
		}
		version.WorkMethod = old.WorkMethod
	}
	data, err := json.Marshal(version)
	if err != nil {
		return "", err
	}
	blob, err := s.object(id, "blob", data)
	if err != nil {
		return "", err
	}
	raw, _ := hex.DecodeString(blob)
	tree, err := s.object(id, "tree", append([]byte("100644 persona.json\x00"), raw...))
	if err != nil {
		return "", err
	}
	body := "tree " + tree + "\n"
	if parent != "" {
		body += "parent " + parent + "\n"
	}
	actor, _ := json.Marshal(person)
	body += "author LINA <lina@example.invalid> 0 +0000\ncommitter LINA <lina@example.invalid> 0 +0000\n\nPersona revision\nPerson: " + string(actor) + "\nAgent: " + id + "\n"
	commit, err := s.object(id, "commit", []byte(body))
	if err != nil {
		return "", err
	}
	if err := s.immutable(filepath.Join(dir, "refs", "revisions", commit), []byte(commit+"\n")); err != nil {
		return "", err
	}
	if parent == "" {
		if err := s.immutable(filepath.Join(dir, "HEAD"), []byte(commit+"\n")); err != nil {
			return "", err
		}
	}
	return commit, ctx.Err()
}
func (s *Store) readObject(id, hash, kind string) ([]byte, error) {
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != 20 || hex.EncodeToString(decoded) != hash || !validID(id) {
		return nil, ErrCorrupt
	}
	path := filepath.Join(repoPath(id), "objects", hash[:2], hash[2:])
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
	content, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(content)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, ErrCorrupt
	}
	header, data, ok := bytes.Cut(content, []byte{0})
	if !ok || string(header) != kind+" "+strconv.Itoa(len(data)) {
		return nil, ErrCorrupt
	}
	return data, nil
}
func (s *Store) load(id, commit string) (Layers, error) {
	version, err := s.loadVersion(id, commit)
	return version.Layers, err
}
func (s *Store) loadVersion(id, commit string) (personaVersion, error) {
	var layers personaVersion
	body, err := s.readObject(id, commit, "commit")
	if err != nil {
		return layers, err
	}
	line, _, ok := strings.Cut(string(body), "\n")
	if !ok || !strings.HasPrefix(line, "tree ") {
		return layers, ErrCorrupt
	}
	tree, err := s.readObject(id, strings.TrimPrefix(line, "tree "), "tree")
	if err != nil {
		return layers, err
	}
	prefix := []byte("100644 persona.json\x00")
	if len(tree) != len(prefix)+20 || !bytes.HasPrefix(tree, prefix) {
		return layers, ErrCorrupt
	}
	data, err := s.readObject(id, hex.EncodeToString(tree[len(prefix):]), "blob")
	if err != nil {
		return layers, err
	}
	err = json.Unmarshal(data, &layers)
	return layers, err
}
