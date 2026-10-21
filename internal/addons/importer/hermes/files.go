// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

var ErrSnapshot = errors.New("invalid_import_snapshot")

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(b)
}

// writeSnapshot creates exclusively; it never truncates or removes a preexisting
// file. Hash verification and cancellation happen before success is returned.
func writeSnapshot(ctx context.Context, destination string, body io.Reader, hash string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, f.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(destination))
		}
	}()
	h := sha256.New()
	if _, err = io.Copy(io.MultiWriter(f, h), contextReader{ctx, body}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if hash != "" && hex.EncodeToString(h.Sum(nil)) != hash {
		return ErrSnapshot
	}
	return f.Sync()
}

// SnapshotSidecar confines the read to the handed-over source root, copies a
// regular file, and verifies the recorded SHA-256. The destination belongs to
// the caller's separate import staging. Missing bytes return os.ErrNotExist so
// the later import stage can record bytes_unavailable without inventing bytes.
func SnapshotSidecar(ctx context.Context, sourceRoot, relative, destination, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(hash)
	if err != nil || len(decoded) != sha256.Size || hex.EncodeToString(decoded) != hash || !filepath.IsLocal(relative) {
		return ErrSnapshot
	}
	if err := outsideSource(sourceRoot, destination); err != nil {
		return err
	}
	root, err := os.OpenRoot(sourceRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrSnapshot
	}
	f, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return ErrSnapshot
	}
	return writeSnapshot(ctx, destination, f, hash)
}

// Staging is caller-owned and separate from the handed-over install. Resolve
// existing parents to reject a destination symlink alias back into the source.
func outsideSource(sourceRoot, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	source, err := filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	source, err = filepath.Abs(source)
	if err != nil {
		return err
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(source, parent)
	if err != nil {
		return err
	}
	if filepath.IsLocal(relative) {
		return ErrReadOnly
	}
	return nil
}
