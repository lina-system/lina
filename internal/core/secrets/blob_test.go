// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/secrets"
)

// A blob keeps an opaque credential such as a messenger session and reveals it
// only through Bytes.
func TestBlobRoundTripIsOpaque(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	key := secrets.Key{Person: "person:owner", Service: "telegram", Name: "session:1001"}
	value := []byte(`{"auth_key":"synthetic-key"}`)
	if err := store.PutBlob(key, value); err != nil {
		t.Fatal(err)
	}
	value[0] = 'X'
	blob, err := store.GetBlob(key)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(blob.Bytes()); got != `{"auth_key":"synthetic-key"}` {
		t.Fatalf("blob = %q", got)
	}
	if strings.Contains(blob.String(), "synthetic-key") {
		t.Fatal("a blob printed its bytes")
	}
	encoded, err := json.Marshal(blob)
	if err != nil || strings.Contains(string(encoded), "synthetic-key") {
		t.Fatalf("a blob serialized its bytes: %s %v", encoded, err)
	}
	copied := blob.Bytes()
	copied[0] = 'Y'
	if again := blob.Bytes(); string(again) == string(copied) {
		t.Fatal("Bytes returned the stored slice")
	}
}

// openTestStore opens the file-backed store on a private state root.
func openTestStore(t *testing.T) *secrets.Store {
	t.Helper()
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := secrets.OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestBlobMissingAndEmptyAreRefused(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	key := secrets.Key{Person: "person:owner", Service: "telegram", Name: "session:1001"}
	if _, err := store.GetBlob(key); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("missing blob error = %v", err)
	}
	if err := store.PutBlob(key, nil); !errors.Is(err, secrets.ErrInvalid) {
		t.Fatalf("empty blob error = %v", err)
	}
}
