// SPDX-License-Identifier: Apache-2.0
package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type created struct {
	path string
	info os.FileInfo
}
type changes struct{ entries []created }

// Only entries created by this attempt can be rolled back. A competing writer's
// replacements and files are preserved, even inside our newly created directory.
func (c *changes) rollback() error {
	var result error
	for i := len(c.entries) - 1; i >= 0; i-- {
		entry := c.entries[i]
		info, err := os.Lstat(entry.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && os.SameFile(info, entry.info) {
			err = os.Remove(entry.path)
		}
		result = errors.Join(result, err)
	}
	return result
}

func absent(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("path already exists: %s", path)
}

func safeParents(path string) error {
	for path != "/" && path != "." {
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && !info.IsDir() {
			return fmt.Errorf("directory must not be linked: %s", path)
		}
		path = filepath.Dir(path)
	}
	return nil
}

func (c *changes) mkdir(path string) error {
	if err := safeParents(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() {
			return errors.New("parent is not a directory")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := c.mkdir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	return c.record(path)
}

func (c *changes) record(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	c.entries = append(c.entries, created{path, info})
	return syncDir(filepath.Dir(path))
}

func (c *changes) write(path string, data []byte, mode os.FileMode) error {
	if err := c.mkdir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	err = c.record(path)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
