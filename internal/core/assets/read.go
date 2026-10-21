// SPDX-License-Identifier: Apache-2.0
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"
)

// MaxReadBytes is the explicit limit for the byte-slice convenience API.
// Larger revisions must be consumed with OpenRead.
const MaxReadBytes = 8 << 20

// OpenRead opens a revision without buffering its contents. The caller owns
// Close and must consume through EOF to verify the size and SHA-256. A mismatch
// replaces EOF with ErrHashMismatch; closing early does not verify the content.
// No store mutex or database transaction is held while the caller consumes it.
func (s *Store) OpenRead(ctx context.Context, id string, rev int64) (io.ReadCloser, error) {
	return s.openRead(ctx, id, rev, -1)
}

func (s *Store) openRead(ctx context.Context, id string, rev, limit int64) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.revision(ctx, id, rev)
	if err != nil {
		return nil, err
	}
	if limit >= 0 && r.Size > limit {
		return nil, ErrTooLarge
	}
	f, err := openRegular(s.root, r.Path)
	if err != nil {
		return nil, err
	}
	return &verifiedReader{file: f, ctx: ctx, digest: sha256.New(), expected: r}, nil
}

type verifiedReader struct {
	file     *os.File
	ctx      context.Context
	digest   hash.Hash
	expected Revision
	size     int64
	terminal error
}

func (r *verifiedReader) Read(p []byte) (int, error) {
	if r.terminal != nil {
		return 0, r.terminal
	}
	if err := r.ctx.Err(); err != nil {
		r.terminal = err
		return 0, err
	}
	n, err := r.file.Read(p)
	r.digest.Write(p[:n])
	r.size += int64(n)
	if err == io.EOF && (r.size != r.expected.Size || hex.EncodeToString(r.digest.Sum(nil)) != r.expected.Hash) {
		err = ErrHashMismatch
	}
	r.terminal = err
	return n, err
}

func (r *verifiedReader) Close() error { return r.file.Close() }

// Read returns a fully verified revision only when it fits in MaxReadBytes.
// It rejects larger recorded sizes before opening the file and also limits the
// actual bytes read, so an unexpectedly enlarged file cannot bypass the cap.
func (s *Store) Read(ctx context.Context, id string, rev int64) ([]byte, error) {
	r, err := s.openRead(ctx, id, rev, MaxReadBytes)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, MaxReadBytes+1))
	if len(data) > MaxReadBytes {
		return nil, ErrTooLarge
	}
	if err != nil {
		return nil, err
	}
	return data, ctx.Err()
}
