// SPDX-License-Identifier: Apache-2.0
package records

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
)

func privateDirectories(root *os.Root, name string) error {
	if !fs.ValidPath(name) {
		return ErrInvalid
	}
	parent := "."
	for _, part := range strings.Split(name, "/") {
		current := path.Join(parent, part)
		if err := root.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return ErrInvalid
		}
		if err := syncDirectory(root, parent); err != nil {
			return err
		}
		parent = current
	}
	return nil
}
func syncDirectory(root *os.Root, name string) error {
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func regular(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrRecordsChanged
	}
	return root.ReadFile(name)
}
func (r *Store) atomic(name string, data []byte) error {
	if err := privateDirectories(r.root, path.Dir(name)); err != nil {
		return err
	}
	tmp := path.Join(".git", ".result-stage-"+newID())
	f, err := r.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.root.Remove(tmp)
	n, err := f.Write(data)
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
	if err = r.root.Rename(tmp, name); err != nil {
		return err
	}
	return syncDirectory(r.root, path.Dir(name))
}
func (r *Store) initialize() error {
	if info, err := r.root.Lstat(".git"); err == nil {
		if !info.IsDir() {
			return ErrRecordsChanged
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := privateDirectories(r.root, ".git/objects"); err != nil {
		return err
	}
	if err := r.atomic(".git/config", []byte("[core]\nrepositoryformatversion = 0\nbare = false\nfilemode = false\n")); err != nil {
		return err
	}
	return r.atomic(".git/HEAD", []byte("ref: refs/heads/records\n"))
}
func (r *Store) object(kind string, data []byte) (string, error) {
	hash, _ := objectID(kind, data)
	name := ".git/objects/" + hash[:2] + "/" + hash[2:]
	if _, err := r.root.Lstat(name); err == nil {
		old, err := r.readObject(hash, kind)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(old, data) {
			return "", ErrRecordsChanged
		}
		return hash, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	compressed, err := encodeObject(kind, data)
	if err != nil {
		return "", err
	}
	return hash, r.atomic(name, compressed)
}
func (r *Store) readObject(hash, kind string) ([]byte, error) {
	h, err := hex.DecodeString(hash)
	if err != nil || len(h) != 20 || hex.EncodeToString(h) != hash {
		return nil, ErrRecordsChanged
	}
	raw, err := regular(r.root, ".git/objects/"+hash[:2]+"/"+hash[2:])
	if err != nil {
		return nil, err
	}
	body, err := decodeObject(kind, raw)
	if err != nil {
		return nil, err
	}
	expected, _ := objectID(kind, body)
	if expected != hash {
		return nil, ErrRecordsChanged
	}
	return body, nil
}
func (r *Store) head() (string, map[string]string, error) {
	head, err := regular(r.root, ".git/HEAD")
	if err != nil || string(head) != "ref: refs/heads/records\n" {
		return "", nil, ErrRecordsChanged
	}
	ref, err := regular(r.root, ".git/refs/heads/records")
	if errors.Is(err, os.ErrNotExist) {
		return "", map[string]string{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	hash := strings.TrimSpace(string(ref))
	commit, err := r.readObject(hash, "commit")
	if err != nil {
		return "", nil, err
	}
	line, _, ok := strings.Cut(string(commit), "\n")
	if !ok || !strings.HasPrefix(line, "tree ") {
		return "", nil, ErrRecordsChanged
	}
	files := map[string]string{}
	err = r.readTree(strings.TrimPrefix(line, "tree "), "", files, 0)
	return hash, files, err
}
func (r *Store) unchanged(files map[string]string) error {
	seen := map[string]bool{}
	err := fs.WalkDir(r.root.FS(), ".", func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == ".git" {
			return fs.SkipDir
		}
		if e.IsDir() {
			return nil
		}
		hash, ok := files[name]
		if !ok || !e.Type().IsRegular() {
			return ErrRecordsChanged
		}
		data, err := regular(r.root, name)
		if err != nil {
			return err
		}
		actual, _ := objectID("blob", data)
		if actual != hash {
			return ErrRecordsChanged
		}
		seen[name] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(files) {
		return ErrRecordsChanged
	}
	return nil
}
