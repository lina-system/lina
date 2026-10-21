// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

func syncDir(root *os.Root, path string) error {
	f, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func privateDir(root *os.Root, path string) error {
	if err := root.MkdirAll(path, 0700); err != nil {
		return err
	}
	current := "."
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return ErrInvalid
		}
	}
	return nil
}
func regularPath(root *os.Root, path string) error {
	if !fs.ValidPath(filepath.ToSlash(path)) {
		return ErrInvalid
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	current := "."
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if i == len(parts)-1 {
			if !info.Mode().IsRegular() {
				return ErrInvalid
			}
		} else if !info.IsDir() {
			return ErrInvalid
		}
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
func fingerprint(ctx context.Context, root *os.Root, path string) (File, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return File{}, err
	}
	entry := File{Path: filepath.ToSlash(path), Mode: uint32(info.Mode().Perm())}
	if info.Mode()&os.ModeSymlink != 0 {
		if !strings.HasPrefix(entry.Path, "workspace/") {
			return File{}, ErrInvalid
		}
		entry.Link, err = root.Readlink(path)
		if err != nil {
			return File{}, err
		}
		h := sha256.Sum256([]byte(entry.Link))
		entry.Hash = hex.EncodeToString(h[:])
		entry.Size = int64(len(entry.Link))
		return entry, nil
	}
	if err = regularPath(root, path); err != nil {
		return File{}, err
	}
	f, err := root.Open(path)
	if err != nil {
		return File{}, err
	}
	defer f.Close()
	h := sha256.New()
	entry.Size, err = io.Copy(h, contextReader{ctx, f})
	if err != nil {
		return File{}, err
	}
	entry.Hash = hex.EncodeToString(h.Sum(nil))
	return entry, nil
}
func copyTree(ctx context.Context, source, target *os.Root, area string, boundary func(string) error, files *[]File, directories *[]string) error {
	if _, err := source.Lstat(area); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return fs.WalkDir(source.FS(), area, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			*directories = append(*directories, path)
			return privateDir(target, path)
		}
		expected, err := fingerprint(ctx, source, path)
		if err != nil {
			return err
		}
		if expected.Link == "" {
			expected.Mode = 0600 | expected.Mode&0111
		}
		*files = append(*files, expected)
		if expected.Link != "" {
			return target.Symlink(expected.Link, path)
		}
		in, err := source.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := target.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|os.FileMode(expected.Mode)&0111)
		if err != nil {
			return err
		}
		_, err = io.Copy(copyWriter{out, boundary}, contextReader{ctx, in})
		if err == nil {
			err = out.Sync()
		}
		err = errors.Join(err, out.Close())
		if err != nil {
			return err
		}
		actual, err := fingerprint(ctx, target, path)
		if err != nil {
			return err
		}
		if expected.Hash != actual.Hash || expected.Size != actual.Size {
			return ErrHashMismatch
		}
		return nil
	})
}
func inventory(ctx context.Context, root *os.Root) ([]File, []string, error) {
	files := []File{}
	dirs := []string{}
	err := fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "manifest.json" {
			return nil
		}
		if d.IsDir() {
			dirs = append(dirs, path)
			return nil
		}
		file, err := fingerprint(ctx, root, path)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	return files, dirs, err
}
func verify(ctx context.Context, root *os.Root, m Manifest) error {
	if m.SchemaVersion != 1 || m.GenerationID == "" || m.MainID == "" || m.CreatedAt.IsZero() || m.Epoch == "" || len(m.Artifacts) == 0 || m.SQLite.Path != "canon/canon.sqlite" || m.SQLite.Revision != m.SQLite.Hash || m.SQLite.SchemaVersion < 1 {
		return ErrInvalid
	}
	files, dirs, err := inventory(ctx, root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(files, m.Files) || !slices.Equal(dirs, m.Directories) {
		return ErrHashMismatch
	}
	found := map[string]File{}
	for _, f := range files {
		found[f.Path] = f
	}
	if found[m.SQLite.Path] != m.SQLite.File {
		return ErrHashMismatch
	}
	for _, a := range m.Assets {
		f, ok := found[filepath.ToSlash(a.Path)]
		if !ok || f.Hash != a.Hash || f.Size != a.Size || !strings.HasPrefix(f.Path, "materials/") {
			return ErrHashMismatch
		}
	}
	repos, err := repositories(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(repos, m.Repositories) {
		return ErrHashMismatch
	}
	derived, err := derivedRevisions(root)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(derived, m.Derived) {
		return ErrHashMismatch
	}
	return ctx.Err()
}

// The boundary is also used by deterministic process-death tests: it runs after
// a chunk reaches the destination, while the copy is still unpublished.
type copyWriter struct {
	w        io.Writer
	boundary func(string) error
}

func (w copyWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if err == nil {
		err = w.boundary("copy_file")
	}
	return n, err
}
