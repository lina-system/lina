// SPDX-License-Identifier: Apache-2.0
package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"syscall"
)

type files struct{ root *os.Root }

// OpenFiles opens the fallback secrets area under an existing private state
// root. Core must hold its state-root lock for the lifetime of this Store and
// choose this backend only when no OS keychain is available.
func OpenFiles(path string) (*Store, error) {
	info, err := os.Lstat(path)
	if err != nil || !private(info, true) {
		return nil, ErrStorage
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrStorage
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !private(opened, true) || !os.SameFile(info, opened) {
		return nil, ErrStorage
	}
	if err := root.Mkdir("secrets", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, ErrStorage
	}
	info, err = root.Lstat("secrets")
	if err != nil || !private(info, true) {
		return nil, ErrStorage
	}
	dir, err := root.OpenRoot("secrets")
	if err != nil {
		return nil, ErrStorage
	}
	opened, err = dir.Stat(".")
	if err != nil || !private(opened, true) || !os.SameFile(info, opened) || syncDir(root) != nil {
		dir.Close()
		return nil, ErrStorage
	}
	return NewStore(&files{dir})
}

func private(info os.FileInfo, directory bool) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	if directory {
		return info.IsDir() && info.Mode().Perm() == 0700
	}
	return info.Mode().IsRegular() && info.Mode().Perm() == 0600 && stat.Nlink == 1
}

func filename(key Key) string {
	encoded, _ := json.Marshal(key)
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (f *files) open(name string) (*os.File, error) {
	file, err := f.root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, ErrStorage
	}
	info, err := file.Stat()
	current, statErr := f.root.Lstat(name)
	if err != nil || statErr != nil || !private(info, false) || !os.SameFile(info, current) {
		file.Close()
		return nil, ErrStorage
	}
	return file, nil
}

func (f *files) Read(key Key) ([]byte, error) {
	file, err := f.open(filename(key))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, maxCredentialBytes+1))
}

func (f *files) checkExisting(name string) error {
	file, err := f.open(name)
	if err != nil {
		return err
	}
	return file.Close()
}

func (f *files) Write(key Key, value []byte) error {
	name := filename(key)
	if err := f.checkExisting(name); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := ".pending-" + hex.EncodeToString(random[:])
	file, err := f.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.root.Remove(tmp)
	_, err = file.Write(value)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err := f.root.Rename(tmp, name); err != nil {
		return err
	}
	return syncDir(f.root)
}

func (f *files) Delete(key Key) error {
	name := filename(key)
	if err := f.checkExisting(name); err != nil {
		return err
	}
	if err := f.root.Remove(name); err != nil {
		return err
	}
	return syncDir(f.root)
}

func (f *files) Close() error { return f.root.Close() }

func syncDir(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
