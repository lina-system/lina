// SPDX-License-Identifier: Apache-2.0
package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func archiveFixture(t *testing.T, headers []*tar.Header) string {
	t.Helper()
	var data bytes.Buffer
	gzipWriter := gzip.NewWriter(&data)
	writer := tar.NewWriter(gzipWriter)
	for _, header := range headers {
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Size > 0 {
			if _, err := writer.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "update.tar.gz")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestArchiveRejectsUnsafeEntries(t *testing.T) {
	for _, name := range []string{"../escape", "/absolute", "a/../../escape", "a\\escape", "a/./file"} {
		t.Run(name, func(t *testing.T) {
			path := archiveFixture(t, []*tar.Header{{Name: name, Mode: 0600, Size: 1}})
			if err := unpack(context.Background(), path, t.TempDir()); err == nil {
				t.Fatal("unsafe entry accepted")
			}
		})
	}
	for _, kind := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo, tar.TypeChar} {
		path := archiveFixture(t, []*tar.Header{{Name: "link", Linkname: "outside", Typeflag: kind, Mode: 0600}})
		if err := unpack(t.Context(), path, t.TempDir()); err == nil {
			t.Fatalf("unsafe type %d accepted", kind)
		}
	}
	path := archiveFixture(t, []*tar.Header{{Name: "lina", Mode: 0700, Size: 1}, {Name: "lina", Mode: 0700, Size: 1}})
	if err := unpack(t.Context(), path, t.TempDir()); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestDigestIsCheckedBeforeUnpack(t *testing.T) {
	path := archiveFixture(t, []*tar.Header{{Name: "lina", Mode: 0700, Size: 1}})
	destination := t.TempDir()
	if _, err := verifiedArchive(t.Context(), path, fmt.Sprintf("%064d", 0), destination); err == nil {
		t.Fatal("digest mismatch accepted")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed download left artifacts: %v %v", entries, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifiedArchive(t.Context(), path, fmt.Sprintf("%x", sha256.Sum256(data)), destination)
	if err != nil {
		t.Fatal(err)
	}
	// Extraction reads the private verified snapshot, not a subsequently changed source.
	if err := os.WriteFile(path, []byte("changed source"), 0600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	if err := unpack(t.Context(), verified, output); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(output, "lina")); err != nil || string(data) != "x" {
		t.Fatal("verified bytes were not used", err)
	}
}
