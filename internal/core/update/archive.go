// SPDX-License-Identifier: Apache-2.0
package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

const archiveLimit int64 = 512 << 20
const unpackLimit int64 = 1 << 30
const fileLimit int64 = 256 << 20

// verifiedArchive freezes the input before any extraction. The digest is from
// the owner's release metadata, not from the untrusted archive itself.
func verifiedArchive(ctx context.Context, source, digest, destination string) (name string, err error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest {
		return "", errors.New("invalid SHA-256 digest")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > archiveLimit {
		return "", errors.New("invalid archive size or type")
	}
	output, err := os.CreateTemp(destination, ".download-")
	if err != nil {
		return "", err
	}
	defer func() {
		err = errors.Join(err, output.Close())
		if err != nil {
			os.Remove(output.Name())
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(output, hash), io.LimitReader(contextReader{ctx, input}, archiveLimit+1))
	if err != nil {
		return "", err
	}
	if n > archiveLimit || fmt.Sprintf("%x", hash.Sum(nil)) != digest {
		return "", errors.New("update digest mismatch")
	}
	if err := output.Sync(); err != nil {
		return "", err
	}
	return output.Name(), nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func unpack(ctx context.Context, archive, destination string) error {
	input, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer input.Close()
	compressed, err := gzip.NewReader(contextReader{ctx, input})
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(io.LimitReader(compressed, unpackLimit+1))
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	seen := map[string]bool{}
	var total int64
	for count := 0; ; count++ {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(header.Name, "/")
		if name == "" || name == "." || path.Clean(name) != name || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || name == ".." || strings.HasPrefix(name, "../") || seen[name] || count >= 4096 {
			return errors.New("unsafe or duplicate update archive entry")
		}
		seen[name] = true
		if header.Typeflag != tar.TypeDir && header.Typeflag != tar.TypeReg {
			return errors.New("update archive permits only regular files and directories")
		}
		if header.Size < 0 || header.Size > fileLimit || total > unpackLimit-header.Size {
			return errors.New("update archive exceeds unpack limit")
		}
		total += header.Size
		if header.Typeflag == tar.TypeDir {
			if err := root.MkdirAll(name, 0700); err != nil {
				return err
			}
			continue
		}
		if err := root.MkdirAll(path.Dir(name), 0700); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if header.Mode&0111 != 0 {
			mode = 0700
		}
		file, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
		if err != nil {
			return err
		}
		_, copyErr := io.CopyN(file, contextReader{ctx, reader}, header.Size)
		syncErr := file.Sync()
		if err := errors.Join(copyErr, syncErr, file.Close()); err != nil {
			return err
		}
	}
	// Consume the trailer so gzip CRC/truncation failures cannot look successful.
	n, err := io.Copy(io.Discard, io.LimitReader(compressed, unpackLimit+1))
	if err != nil {
		return err
	}
	if n > unpackLimit {
		return errors.New("update archive trailing data exceeds limit")
	}
	return ctx.Err()
}
