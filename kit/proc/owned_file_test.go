// SPDX-License-Identifier: Apache-2.0

//go:build unix

package proc

import (
	"bytes"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// TestOwnedFileCopyStopsWhileTheWriterStaysOpen is the file-backed input
// contract. Stopping the relay returns while the writer end is still open,
// and bytes written after that stay with the caller. The caller's file stays
// open.
func TestOwnedFileCopyStopsWhileTheWriterStaysOpen(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()

	got := make(chan struct{})
	var once sync.Once
	dst := writerFunc(func(p []byte) (int, error) {
		if len(p) > 0 {
			once.Do(func() { close(got) })
		}
		return len(p), nil
	})
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- copyOwnedFile(dst, reader, stop) }()

	if _, err := writer.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not receive the first write")
	}
	close(stop)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("file relay still blocked after stop")
	}
	if _, err := writer.Write([]byte("two")); err != nil {
		t.Fatal(err)
	}
	if err := reader.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	n, err := reader.Read(buf)
	if err != nil || string(buf[:n]) != "two" {
		t.Fatalf("caller read %q, %v; the finished relay still owns the file", buf[:n], err)
	}
}

// TestOwnedFileCopyEndsWhenTheWriterCloses keeps the ordinary end of input:
// the writer closing still delivers every byte and returns.
func TestOwnedFileCopyEndsWhenTheWriterCloses(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- copyOwnedFile(&buf, reader, make(chan struct{})) }()
	if _, err := writer.Write([]byte("data")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("copy did not finish at EOF")
	}
	if buf.String() != "data" {
		t.Fatalf("copied %q", buf.String())
	}
}

// TestOwnedFileCopyReadsARegularFileToEOF covers a file that cannot be
// interrupted by a deadline. The copy reads it to the end and leaves it open.
func TestOwnedFileCopyReadsARegularFileToEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var buf bytes.Buffer
	if err := copyOwnedFile(&buf, file, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "abc" {
		t.Fatalf("copied %q", buf.String())
	}
	if _, err := file.Stat(); err != nil {
		t.Fatal(err)
	}
}
