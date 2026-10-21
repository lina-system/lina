// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"errors"
	"os"
	"sync"
	"syscall"
)

// The emergency space is outside SQLite so unlinking it needs no database
// transaction. Writing every byte (not Truncate) allocates real disk blocks.
const reserveBytes = 256 * 1024

type Reserve struct {
	mu       sync.Mutex
	root     *os.Root
	closed   bool
	present  bool
	allocate func(*os.File) error
}

// PrepareReserve creates only system/events in an existing private state root.
// It never opens canon or materials. Keep the reserve open for the store's life.
// Allocation is best effort on a full disk: a consumed reserve must not stop
// diagnosis reads after restart. Present reports whether allocation succeeded.
func PrepareReserve(stateRoot string) (*Reserve, error) {
	return prepareReserve(stateRoot, allocateReserve)
}

func prepareReserve(stateRoot string, allocate func(*os.File) error) (*Reserve, error) {
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, ErrStorage
	}
	defer root.Close()
	if err := root.MkdirAll("system/events", 0700); err != nil {
		return nil, ErrStorage
	}
	events, err := root.OpenRoot("system/events")
	if err != nil {
		return nil, ErrStorage
	}
	r := &Reserve{root: events, allocate: allocate}
	if err := r.replenish(); err != nil && !errors.Is(err, syscall.ENOSPC) {
		events.Close()
		return nil, ErrStorage
	}
	return r, nil
}

func (r *Reserve) syncDirectory() error {
	f, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Present reports whether this handle has a complete, allocated reserve.
// A failed allocation or release never reports the reserve as restored.
func (r *Reserve) Present() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.present
}

// inspect validates a handle at Open without creating or growing a file.
func (r *Reserve) inspect() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	return r.inspectLocked()
}

func (r *Reserve) inspectLocked() error {
	r.present = false
	info, err := r.root.Lstat("reserve")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > reserveBytes {
		return ErrStorage
	}
	r.present = info.Size() == reserveBytes
	return nil
}

// Replenish explicitly restores a consumed reserve after space is recovered.
// It reports allocation errors even when startup would tolerate a full disk.
func (r *Reserve) Replenish() error {
	if err := r.replenish(); err != nil {
		if errors.Is(err, ErrClosed) {
			return ErrClosed
		}
		return ErrStorage
	}
	return nil
}

// replenish retains the OS error only for startup's full-disk classification.
func (r *Reserve) replenish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if err := r.inspectLocked(); err != nil {
		return err
	}
	if r.present {
		return nil
	}
	f, err := r.root.OpenFile("reserve", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = r.allocate(f); err == nil {
		err = r.syncDirectory()
	}
	if err != nil {
		// A partial allocation must not look like usable emergency space on
		// the next Open. Only remove the validated reserve file we just wrote.
		if removeErr := r.root.Remove("reserve"); removeErr != nil {
			return removeErr
		}
		if syncErr := r.syncDirectory(); syncErr != nil {
			return syncErr
		}
		return err
	}
	r.present = true
	return nil
}

func allocateReserve(f *os.File) error {
	if _, err := f.WriteAt(make([]byte, reserveBytes), 0); err != nil {
		return err
	}
	return f.Sync()
}

// Release consumes the reserve. An absent file is already released; other
// removal errors and directory sync failures still report storage failure.
func (r *Reserve) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if err := r.root.Remove("reserve"); err != nil && !os.IsNotExist(err) {
		return ErrStorage
	}
	r.present = false
	if err := r.syncDirectory(); err != nil {
		return ErrStorage
	}
	return nil
}
func (r *Reserve) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.root.Close()
}
