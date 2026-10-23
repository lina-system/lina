// SPDX-License-Identifier: Apache-2.0
//go:build linux || darwin

package repocache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// A busy cache is a named wait, not a concurrent mutation. Kernel locks are
// released on process death; a stale lock file never makes the cache unusable.
func (c *Cache) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(c.root, "cache.lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "cache.lock")
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return nil, errors.New("repository cache is busy")
	}
	c.lockFile = file
	return func() { c.lockFile = nil; file.Close() }, nil
}
