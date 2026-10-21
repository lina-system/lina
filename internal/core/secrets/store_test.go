// SPDX-License-Identifier: Apache-2.0
package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestFileStorePersistenceIsolationAndTurnGuard(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := secrets.OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	key := secrets.Key{Person: "owner", Service: "plugin", Name: "token"}
	if err := s.Put(key, []byte("syntheticcredential")); err != nil {
		t.Fatal(err)
	}
	value, err := s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	env, err := secret.Environment("TOKEN", value)
	if err != nil || env != "TOKEN=syntheticcredential" {
		t.Fatal("store read failed")
	}
	other := key
	other.Person = "someone-else"
	if _, err := s.Get(other); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("credential crossed person boundary")
	}
	finish := s.BeginTurn()
	if err := s.Put(key, []byte("replacement")); !errors.Is(err, secrets.ErrInTurn) {
		t.Fatal("rotation allowed in turn")
	}
	if err := s.Delete(key); !errors.Is(err, secrets.ErrInTurn) {
		t.Fatal("deletion allowed in turn")
	}
	finish()
	finish()
	if err := s.Put(key, []byte("replacement")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(key); !errors.Is(err, secrets.ErrClosed) {
		t.Fatal("closed store read")
	}
	s, err = secrets.OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	value, err = s.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	env, err = secret.Environment("TOKEN", value)
	if err != nil || env != "TOKEN=replacement" {
		t.Fatal("rotation did not persist")
	}
	entries, err := os.ReadDir(filepath.Join(path, "secrets"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("unexpected store files: %d", len(entries))
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 || strings.Contains(info.Name(), "owner") {
		t.Fatal("unsafe credential file")
	}
	if err := s.Delete(key); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(key); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("deleted credential remains")
	}
}

func TestFileStoreRefusesUnsafePaths(t *testing.T) {
	for _, kind := range []string{"mode", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			path := t.TempDir()
			if err := os.Chmod(path, 0700); err != nil {
				t.Fatal(err)
			}
			s, err := secrets.OpenFiles(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			key := secrets.Key{Person: "owner", Service: "account", Name: "token"}
			if err := s.Put(key, []byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			entries, _ := os.ReadDir(filepath.Join(path, "secrets"))
			file := filepath.Join(path, "secrets", entries[0].Name())
			switch kind {
			case "mode":
				err = os.Chmod(file, 0644)
			case "symlink":
				target := filepath.Join(path, "target")
				err = os.Rename(file, target)
				if err == nil {
					err = os.Symlink(target, file)
				}
			case "hardlink":
				err = os.Link(file, filepath.Join(path, "alias"))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Get(key); err == nil {
				t.Fatal("unsafe credential read")
			}
			if err := s.Put(key, []byte("replacement")); err == nil {
				t.Fatal("unsafe credential replaced")
			}
		})
	}
	path := t.TempDir()
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if s, err := secrets.OpenFiles(path); err == nil {
		s.Close()
		t.Fatal("public root accepted")
	}
}

type failingKeychain struct{ closed bool }

func (*failingKeychain) Read(secrets.Key) ([]byte, error) {
	return []byte("syntheticcredential"), errors.New("syntheticcredential provider failure")
}
func (*failingKeychain) Write(secrets.Key, []byte) error {
	return errors.New("syntheticcredential provider failure")
}
func (*failingKeychain) Delete(secrets.Key) error {
	return errors.New("syntheticcredential provider failure")
}
func (b *failingKeychain) Close() error { b.closed = true; return nil }

func TestKeychainFailureIsSanitizedAndDoesNotFallback(t *testing.T) {
	backend := &failingKeychain{}
	s, err := secrets.NewStore(backend)
	if err != nil {
		t.Fatal(err)
	}
	key := secrets.Key{Person: "owner", Service: "account", Name: "token"}
	value, err := s.Get(key)
	if err != secrets.ErrStorage || value.String() != secret.Placeholder {
		t.Fatal("keychain failure escaped boundary")
	}
	if err := s.Put(key, []byte("syntheticcredential")); err != secrets.ErrStorage {
		t.Fatal("keychain write failure escaped boundary")
	}
	if err := s.Delete(key); err != secrets.ErrStorage {
		t.Fatal("keychain delete failure escaped boundary")
	}
	if err := s.Close(); err != nil || !backend.closed {
		t.Fatal("backend not closed")
	}
}

func TestOverlappingTurnsAndCredentialValidation(t *testing.T) {
	path := t.TempDir()
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := secrets.OpenFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := secrets.Key{Person: "owner", Service: "account", Name: "token"}
	release1, release2 := s.BeginTurn(), s.BeginTurn()
	release1()
	release1()
	if err := s.Put(key, []byte("value")); err != secrets.ErrInTurn {
		t.Fatal("one release unpinned overlapping turn")
	}
	release2()
	if err := s.Put(key, []byte("value")); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []secrets.Key{{Person: string([]byte{0xff}), Service: "account", Name: "token"}, {}, {Person: "owner", Service: "account"}, {Person: "owner", Service: "account", Name: "bad\x00key"}} {
		if err := s.Put(invalid, []byte("value")); err != secrets.ErrInvalid {
			t.Fatal("invalid key admitted")
		}
	}
	for _, invalid := range [][]byte{nil, make([]byte, 65537)} {
		if err := s.Put(key, invalid); err != secrets.ErrInvalid {
			t.Fatal("invalid value admitted")
		}
	}
	other := key
	other.Service = "channel"
	if _, err := s.Get(other); err != secrets.ErrNotFound {
		t.Fatal("credential crossed service boundary")
	}
}
