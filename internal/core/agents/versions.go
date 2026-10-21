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
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
func immutable(root *os.Root, path string, data []byte) error {
	if err := privateDirs(root, filepath.Dir(path)); err != nil {
		return err
	}
	f, err := root.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		info, e := root.Lstat(path)
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() {
			return ErrInvalid
		}
		old, e := root.ReadFile(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(old, data) {
			return ErrCorrupt
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	return syncDir(root, filepath.Dir(path))
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
	err := immutable(s.root, filepath.Join(repoPath(id), "objects", hash[:2], hash[2:]), compressed.Bytes())
	return hash, err
}
func (s *Store) save(ctx context.Context, id, parent string, layers Layers, person string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validID(id) {
		return "", ErrInvalid
	}
	dir := repoPath(id)
	if err := immutable(s.root, filepath.Join(dir, "config"), []byte("[core]\nrepositoryformatversion = 0\nbare = true\n")); err != nil {
		return "", err
	}
	data, _ := json.Marshal(layers)
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
	if err := immutable(s.root, filepath.Join(dir, "refs", "revisions", commit), []byte(commit+"\n")); err != nil {
		return "", err
	}
	if parent == "" {
		if err := immutable(s.root, filepath.Join(dir, "HEAD"), []byte(commit+"\n")); err != nil {
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
	var layers Layers
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
