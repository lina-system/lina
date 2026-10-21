// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func newID() string {
	var id [16]byte
	_, _ = rand.Read(id[:])
	return hex.EncodeToString(id[:])
}

func validID(id string) bool {
	b, err := hex.DecodeString(id)
	return err == nil && len(b) == 16 && hex.EncodeToString(b) == id
}

func syncDir(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// Only private, real directories belong to this store. Root also confines every
// access if an ancestor is exchanged concurrently.
func mkdirs(root *os.Root, path string) error {
	if path == "." {
		return nil
	}
	if !filepath.IsLocal(path) {
		return ErrInvalid
	}
	parent := "."
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		name := filepath.Join(parent, part)
		err := root.Mkdir(name, 0700)
		if err != nil && !errors.Is(err, os.ErrExist) {
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

type contextReader struct {
	ctx context.Context
	io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func openRegular(root *os.Root, path string) (*os.File, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrInvalid
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.Join(ErrInvalid, err)
	}
	return f, nil
}

func writeFile(ctx context.Context, root *os.Root, path string, body io.Reader) (hash string, size int64, err error) {
	if body == nil {
		return "", 0, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if err := mkdirs(root, filepath.Dir(path)); err != nil {
		return "", 0, err
	}
	f, err := root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeFile(root, path))
		}
	}()
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(f, h), contextReader{ctx, body})
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err == nil {
		err = syncDir(root, filepath.Dir(path))
	}
	return hex.EncodeToString(h.Sum(nil)), size, err
}

func removeFile(root *os.Root, path string) error {
	err := root.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDir(root, filepath.Dir(path))
}

func verifyFile(ctx context.Context, root *os.Root, r Revision) error {
	if !validRevision(r) {
		return ErrInvalid
	}
	f, err := openRegular(root, r.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, contextReader{ctx, f})
	if err != nil {
		return err
	}
	if size != r.Size || hex.EncodeToString(h.Sum(nil)) != r.Hash {
		return fmt.Errorf("%w: asset %s revision %d", ErrHashMismatch, r.AssetID, r.Revision)
	}
	return ctx.Err()
}

func revisionPath(area Area, id string, rev int64) string {
	return filepath.Join("materials", string(area), id, fmt.Sprint(rev), "content")
}

func validRevision(r Revision) bool {
	hash, err := hex.DecodeString(r.Hash)
	return validID(r.AssetID) && r.Revision > 0 && r.Size >= 0 && err == nil && len(hash) == sha256.Size &&
		(r.Path == revisionPath(User, r.AssetID, r.Revision) || r.Path == revisionPath(Lina, r.AssetID, r.Revision))
}
