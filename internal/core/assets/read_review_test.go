// SPDX-License-Identifier: Apache-2.0
package assets_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestReadRefusesOversizedBuffer(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	// The byte-slice convenience API is capped at 8 MiB; larger material must
	// use a stream. Generate the fixture without allocating its full contents.
	token, err := s.StageImport(t.Context(), io.LimitReader(zeroReader{}, assets.MaxReadBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Handover(t.Context(), token, "video.bin", assets.User, actor)
	if err != nil {
		t.Fatal(err)
	}
	data, err := s.Read(t.Context(), a.ID, 1)
	if !errors.Is(err, assets.ErrTooLarge) || data != nil {
		t.Fatalf("oversized read returned %d bytes, %v", len(data), err)
	}
	r := openStream(t, s, a.ID)
	if size, err := io.Copy(io.Discard, r); size != assets.MaxReadBytes+1 || err != nil {
		t.Fatalf("large streaming read: size=%d err=%v", size, err)
	}
}

func openStream(t *testing.T, s *assets.Store, id string) io.ReadCloser {
	t.Helper()
	r, err := s.OpenRead(t.Context(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestReadLimitChecksActualBytesAndAllowsBoundary(t *testing.T) {
	t.Parallel()
	s, _, root := setup(t)
	token, err := s.StageImport(t.Context(), io.LimitReader(zeroReader{}, assets.MaxReadBytes))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Handover(t.Context(), token, "boundary.bin", assets.User, actor)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := s.Read(t.Context(), a.ID, 1); err != nil || len(data) != assets.MaxReadBytes {
		t.Fatalf("boundary read: %d bytes, %v", len(data), err)
	}
	if err := os.Truncate(filepath.Join(root, a.Path), assets.MaxReadBytes+1); err != nil {
		t.Fatal(err)
	}
	if data, err := s.Read(t.Context(), a.ID, 1); !errors.Is(err, assets.ErrTooLarge) || data != nil {
		t.Fatalf("enlarged read: %d bytes, %v", len(data), err)
	}
}

func TestOpenReadHonorsCancellation(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := importAsset(t, s, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r, err := s.OpenRead(ctx, a.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var one [1]byte
	if _, err := r.Read(one[:]); err != nil {
		t.Fatal(err)
	}
	cancel()
	if n, err := r.Read(one[:]); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: n=%d err=%v", n, err)
	}
}

func TestOpenReadVerifiesAtEOF(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, bytes string
		mismatch    bool
	}{
		{"valid", "original", false},
		{"same_size_corruption", "modified", true},
		{"truncated", "ori", true},
		{"extended", "original!", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, _, root := setup(t)
			a := importAsset(t, s, "original")
			if err := os.WriteFile(filepath.Join(root, a.Path), []byte(test.bytes), 0600); err != nil {
				t.Fatal(err)
			}
			r := openStream(t, s, a.ID)
			// A stream returns bytes before EOF; callers must consume through
			// its terminal result to establish integrity.
			var first [1]byte
			if n, err := r.Read(first[:]); n != 1 || err != nil || first[0] != test.bytes[0] {
				t.Fatalf("first read: n=%d err=%v", n, err)
			}
			tail, err := io.ReadAll(r)
			if string(append(first[:], tail...)) != test.bytes {
				t.Fatal("stream changed bytes")
			}
			if test.mismatch && !errors.Is(err, assets.ErrHashMismatch) {
				t.Fatalf("corrupt EOF: %v", err)
			}
			if !test.mismatch && err != nil {
				t.Fatalf("valid EOF: %v", err)
			}
			_, terminal := r.Read(first[:])
			want := io.EOF
			if test.mismatch {
				want = assets.ErrHashMismatch
			}
			if !errors.Is(terminal, want) {
				t.Fatalf("terminal result: %v, want %v", terminal, want)
			}
		})
	}
}

func TestOpenReadDoesNotHoldStoreLock(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	a := importAsset(t, s, "streaming")
	r := openStream(t, s, a.ID)
	var first [1]byte
	if _, err := r.Read(first[:]); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := s.Rename(t.Context(), a.ID, "renamed.bin", actor); finished <- err }()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second): // Deadlock watchdog, not a timing-based success condition.
		r.Close()
		<-finished
		t.Fatal("asset operation blocked on an unfinished reader")
	}
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
}
