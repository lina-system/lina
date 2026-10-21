// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

var ErrRestorePending = errors.New("restore incomplete; repeat lina restore before starting Core")

const restorePending = "staging/restore.pending"

// CheckRestore must run while holding the Core lock, before opening canon or
// serving a client. An interrupted multi-area publication never becomes a
// readable mixture of generations. Control records are not used as a journal.
func CheckRestore(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	if info, err := root.Lstat("staging"); err == nil {
		if !info.IsDir() {
			return ErrRestorePending
		}
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else {
		return err
	}
	if _, err := root.Lstat(restorePending); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return ErrRestorePending
}

// Restore publishes the replayed removal list before canon and keeps the old
// areas under the pending directory until all replacements are durable.
var restoreAreas = []string{"backups/removals", "versions", "materials", "derived", "workspace", "canon"}

func restoreSync(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func restoreBoundary(after func(string) error, name string) error {
	if after != nil {
		return after(name)
	}
	return nil
}

func commitRestore(root *os.Root, prepared string, after func(string) error) error {
	if _, err := root.Lstat(restorePending); !errors.Is(err, os.ErrNotExist) {
		return ErrRestorePending
	}
	if err := root.Rename(prepared, restorePending); err != nil {
		return err
	}
	if err := restoreSync(root, "staging"); err != nil {
		return err
	}
	if err := restoreBoundary(after, "pending"); err != nil {
		return err
	}
	return resumeRestore(root, after)
}

func resumeRestore(root *os.Root, after func(string) error) error {
	for _, area := range restoreAreas {
		next := restorePending + "/new/" + area
		previous := restorePending + "/previous/" + area
		nextInfo, err := root.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			// The prepared entry is absent only after its rename into place.
			if _, err := root.Lstat(area); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if area == "backups/removals" {
			if !nextInfo.Mode().IsRegular() {
				return ErrRestorePending
			}
		} else if !nextInfo.IsDir() {
			return ErrRestorePending
		}
		_, err = root.Lstat(previous)
		if errors.Is(err, os.ErrNotExist) {
			if info, err := root.Lstat(area); err == nil {
				if info.Mode()&os.ModeSymlink != 0 {
					return ErrRestorePending
				}
				if err := root.Rename(area, previous); err != nil {
					return err
				}
				if err := errors.Join(restoreSync(root, filepath.Dir(area)), restoreSync(root, filepath.Dir(previous))); err != nil {
					return err
				}
				if err := restoreBoundary(after, "saved:"+area); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else if err != nil {
			return err
		}
		// A path unexpectedly reappearing after the original was saved is never
		// overwritten; only this restore's absent publication slots are consumed.
		if _, err := root.Lstat(area); !errors.Is(err, os.ErrNotExist) {
			return ErrRestorePending
		}
		if err := root.Rename(next, area); err != nil {
			return err
		}
		if err := errors.Join(restoreSync(root, filepath.Dir(next)), restoreSync(root, filepath.Dir(area))); err != nil {
			return err
		}
		if err := restoreBoundary(after, "installed:"+area); err != nil {
			return err
		}
	}
	retired := "staging/restore.retired-" + rand.Text()
	if err := root.Rename(restorePending, retired); err != nil {
		return err
	}
	if err := restoreSync(root, "staging"); err != nil {
		return err
	}
	// This directory contains only the previous areas captured by this restore.
	// If cleanup fails it is inert; Core no longer sees a pending publication.
	if err := root.RemoveAll(retired); err != nil {
		return err
	}
	return restoreSync(root, "staging")
}
