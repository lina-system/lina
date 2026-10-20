// SPDX-License-Identifier: Apache-2.0
package eventstore

import (
	"os"
	"sync"
)

// The emergency space is outside SQLite so unlinking it needs no database
// transaction. Writing every byte (not Truncate) allocates real disk blocks.
const reserveBytes = 256 * 1024

type Reserve struct {
	mu     sync.Mutex
	root   *os.Root
	closed bool
}

// PrepareReserve creates only system/events in an existing private state root.
// It never opens canon or materials. Keep the reserve open for the store's life.
func PrepareReserve(stateRoot string) (*Reserve, error) {
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
	r := &Reserve{root: events}
	if err := r.Replenish(); err != nil {
		events.Close()
		return nil, err
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

// Replenish is called at startup and explicitly after disk space is recovered.
// A consumed reserve stays consumed; writes never claim it has been restored.
func (r *Reserve) Replenish() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	// Reject a symlink or non-regular preexisting reserve before touching it.
	if info, err := r.root.Lstat("reserve"); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > reserveBytes {
			return ErrStorage
		}
	} else if !os.IsNotExist(err) {
		return ErrStorage
	}
	f, err := r.root.OpenFile("reserve", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return ErrStorage
	}
	defer f.Close()
	if _, err = f.WriteAt(make([]byte, reserveBytes), 0); err != nil {
		return ErrStorage
	}
	if err = f.Sync(); err != nil {
		return ErrStorage
	}
	if err = r.syncDirectory(); err != nil {
		return ErrStorage
	}
	return nil
}
func (r *Reserve) Release() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	if err := r.root.Remove("reserve"); err != nil {
		return ErrStorage
	}
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
