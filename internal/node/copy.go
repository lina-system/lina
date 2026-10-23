// SPDX-License-Identifier: Apache-2.0

// Package node implements Node-side execution helpers. Callers must establish
// a live grant before transferring inputs or making them available to work.
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

var (
	ErrInvalidCopy      = errors.New("invalid_file_copy")
	ErrCopyHashMismatch = errors.New("file_copy_hash_mismatch")
)

// Copy owns one disposable input, never a canonical file or persistent cache.
// Close removes its bytes. Its path is exposed only after complete verification.
type Copy struct {
	directory, path string
	mu              sync.Mutex
}

func (c *Copy) Path() string { return c.path }

func (c *Copy) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directory == "" {
		return nil
	}
	if err := os.RemoveAll(c.directory); err != nil {
		return err
	}
	c.directory = ""
	return nil
}

// ReceiveCopy stages a stream in a private directory, checks its complete size
// and SHA-256, then closes the writable descriptor and returns a read-only file.
// On failure no path is returned and staging is removed. Hash/size come from the
// authorized revision, not the transfer. Grant and transport ownership belong
// to the caller; this helper does not issue, persist or extend authorization.
func ReceiveCopy(ctx context.Context, directory, hash string, size int64, source io.Reader) (copy *Copy, err error) {
	digest, decodeErr := hex.DecodeString(hash)
	if decodeErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != hash || size < 0 || size == math.MaxInt64 || source == nil {
		return nil, ErrInvalidCopy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	private, err := os.MkdirTemp(directory, ".copy-")
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(private))
		}
	}()
	path := filepath.Join(private, hash)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(copyReader{ctx, source}, size+1))
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && (n != size || hex.EncodeToString(h.Sum(nil)) != hash) {
		err = ErrCopyHashMismatch
	}
	if err == nil {
		err = f.Chmod(0400)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return &Copy{directory: private, path: path}, nil
}

type copyReader struct {
	ctx context.Context
	io.Reader
}

func (r copyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}
