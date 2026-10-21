// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/secret"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("../../../testdata/hermes", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func sourceDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "source ?#.sqlite")
	db, err := canon.OpenSQLite(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.ExecContext(t.Context(), string(fixture(t, "synthetic.sql"))); err != nil {
		t.Fatal(err)
	}
	return p, db
}
func syntheticSchema() SQLiteSchema {
	return SQLiteSchema{Version: 7, Tables: map[string][]Column{
		"messages": {{Name: "id", Type: "INTEGER", PrimaryKey: 1}, {Name: "body", Type: "TEXT", NotNull: true}},
	}}
}
func fileBytes(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func TestSQLiteSnapshotIncludesCommittedWALAndNeverWritesSource(t *testing.T) {
	path, db := sourceDB(t)
	before := fileBytes(t, path)
	wal := fileBytes(t, path+"-wal")
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(t.Context(), "INSERT INTO messages VALUES (2, 'uncommitted')"); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err = SnapshotSQLite(t.Context(), path, dest, syntheticSchema()); err != nil {
		t.Fatal(err)
	}
	reader, err := canon.OpenSQLiteReader(t.Context(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var body string
	var count int
	if err = reader.QueryRow("SELECT body FROM messages").Scan(&body); err != nil || body != "synthetic committed message" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if err = reader.QueryRow("SELECT count(*) FROM messages").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if !bytes.Equal(before, fileBytes(t, path)) || !bytes.Equal(wal, fileBytes(t, path+"-wal")) {
		t.Fatal("source bytes changed")
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("snapshot is not private", err)
	}
	if err = SnapshotSQLite(t.Context(), path, path, syntheticSchema()); !errors.Is(err, os.ErrExist) {
		t.Fatalf("source overwrite: %v", err)
	}
}

func TestSQLiteGateRefusesUnknownBeforePublishingSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, change, difference string }{
		{"version", "PRAGMA user_version=8", "user_version"},
		{"column", "ALTER TABLE messages ADD COLUMN extra TEXT", "messages"},
		{"missing", "DROP TABLE messages", "messages"},
		{"table", "CREATE TABLE unknown(value TEXT)", "unknown"},
		{"view", "CREATE VIEW unknown AS SELECT * FROM messages", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, db := sourceDB(t)
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(t.TempDir(), "snapshot.sqlite")
			err := SnapshotSQLite(t.Context(), path, dest, syntheticSchema())
			if !errors.Is(err, ErrSchema) || !strings.Contains(err.Error(), tc.difference) {
				t.Fatalf("difference not reported: %v", err)
			}
			if _, err = os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rejected source left snapshot")
			}
		})
	}
}

func TestSQLiteFailureDoesNotCreateSourceOrOverwriteDestination(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite")
	dest := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err := SnapshotSQLite(t.Context(), missing, dest, syntheticSchema()); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created source")
	}
	path, _ := sourceDB(t)
	if err := os.WriteFile(dest, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotSQLite(t.Context(), path, dest, syntheticSchema()); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	if string(fileBytes(t, dest)) != "keep" {
		t.Fatal("overwrote destination")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := SnapshotSQLite(ctx, path, dest, syntheticSchema()); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := SnapshotSQLite(t.Context(), path, dest, SQLiteSchema{}); !errors.Is(err, ErrSchema) {
		t.Fatal("empty schema accepted", err)
	}
}

func TestHonchoSchemaGateAndRawGETSnapshot(t *testing.T) {
	api := fixture(t, "openapi-synthetic.json")
	raw := []byte("{\"items\": [{\"id\": \"synthetic-1\", \"body\":\"hello\"}], \"next\":null}\n")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer synthetic-read-credential" {
			t.Error("missing read credential")
		}
		if r.URL.Path == "/openapi.json" {
			w.Write(api)
		} else {
			w.Write(raw)
		}
	}))
	defer srv.Close()
	reader, err := OpenHoncho(t.Context(), srv.URL, "/openapi.json", api, secret.New("synthetic-read-credential"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	dest := filepath.Join(t.TempDir(), "page.json")
	if err = reader.Snapshot(t.Context(), "/workspaces/synthetic/messages?page=2", dest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, fileBytes(t, dest)) {
		t.Fatal("response was rewritten")
	}
	if want := []string{"GET /openapi.json", "GET /workspaces/synthetic/messages?page=2"}; !reflect.DeepEqual(calls, want) {
		t.Fatal(calls)
	}
	for _, path := range []string{"/write-only", srv.URL + "/workspaces/synthetic/messages", "//other.invalid/messages", "/unknown", "/workspaces/../messages", "/workspaces/%2e%2e/messages"} {
		if err = reader.Snapshot(t.Context(), path, filepath.Join(t.TempDir(), "refused.json")); err == nil {
			t.Fatal("accepted path", path)
		}
	}
	if len(calls) != 2 {
		t.Fatal("refused path reached server", calls)
	}
	reader.Close()
	if err = reader.Snapshot(t.Context(), "/workspaces/synthetic/messages", filepath.Join(t.TempDir(), "closed.json")); err == nil {
		t.Fatal("closed reader used credential")
	}
}

func TestHonchoUnknownSchemaDoesNotReadData(t *testing.T) {
	expected := fixture(t, "openapi-synthetic.json")
	for _, body := range [][]byte{
		bytes.ReplaceAll(expected, []byte(`"synthetic-1"`), []byte(`"synthetic-2"`)),
		bytes.ReplaceAll(expected, []byte(`"string"`), []byte(`"integer"`)),
		[]byte(`{"openapi":"3.1.0"}`), []byte("invalid"),
	} {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.Write(body) }))
		reader, err := OpenHoncho(t.Context(), srv.URL, "/openapi.json", expected, secret.New("synthetic"))
		if reader != nil {
			reader.Close()
		}
		srv.Close()
		if !errors.Is(err, ErrSchema) || calls != 1 {
			t.Fatalf("gate err=%v calls=%d", err, calls)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGETOnlyTransportRejectsMutationsBeforeNetwork(t *testing.T) {
	calls := 0
	tr := GETOnlyTransport{Base: roundTripFunc(func(r *http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("reached network") })}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "CONNECT", "get", ""} {
		req, _ := http.NewRequest("GET", "https://example.invalid/", nil)
		req.Method = method
		if _, err := tr.RoundTrip(req); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("method=%q err=%v", method, err)
		}
	}
	if calls != 0 {
		t.Fatal("mutation reached network")
	}
}

func TestHonchoRedirectAndFailureNeverPublish(t *testing.T) {
	api := fixture(t, "openapi-synthetic.json")
	for _, status := range []int{301, 302, 303, 307, 308, 401, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			reached := 0
			other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++ }))
			defer other.Close()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/openapi.json" {
					w.Write(api)
					return
				}
				w.Header().Set("Location", other.URL)
				w.WriteHeader(status)
				w.Write([]byte("secret response must not become an error"))
			}))
			defer srv.Close()
			reader, err := OpenHoncho(t.Context(), srv.URL, "/openapi.json", api, secret.New("synthetic"))
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			dest := filepath.Join(t.TempDir(), "page.json")
			err = reader.Snapshot(t.Context(), "/workspaces/synthetic/messages", dest)
			if err == nil || strings.Contains(err.Error(), "secret response") || reached != 0 {
				t.Fatalf("err=%v forwarded=%d", err, reached)
			}
			if _, err = os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed response was published")
			}
		})
	}
}

func TestSidecarCopyVerifiesHashAndLeavesSourceUntouched(t *testing.T) {
	root := t.TempDir()
	body := fixture(t, "sidecar.txt")
	src := filepath.Join(root, "output.txt")
	if err := os.WriteFile(src, body, 0400); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	dest := filepath.Join(t.TempDir(), "sidecar")
	if err := SnapshotSidecar(t.Context(), root, "output.txt", dest, hash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, fileBytes(t, dest)) || !bytes.Equal(body, fileBytes(t, src)) {
		t.Fatal("changed bytes")
	}
	for _, tc := range []struct{ path, hash string }{{"output.txt", strings.Repeat("0", 64)}, {"../outside", hash}, {"missing", hash}} {
		failed := filepath.Join(t.TempDir(), "failed")
		if err := SnapshotSidecar(t.Context(), root, tc.path, failed, tc.hash); err == nil {
			t.Fatal("accepted invalid sidecar")
		}
		if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("failed copy left bytes")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, body, 0600)
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotSidecar(t.Context(), root, "link", filepath.Join(t.TempDir(), "escape"), hash); err == nil {
		t.Fatal("followed escaped symlink")
	}
}

func TestSnapshotDestinationMustBeOutsideSource(t *testing.T) {
	path, _ := sourceDB(t)
	destination := filepath.Join(filepath.Dir(path), "new.sqlite")
	if err := SnapshotSQLite(t.Context(), path, destination, syntheticSchema()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("source directory accepted as staging: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote into source directory")
	}
	root := t.TempDir()
	body := []byte("synthetic")
	os.WriteFile(filepath.Join(root, "input"), body, 0600)
	hash := fmt.Sprintf("%x", sha256.Sum256(body))
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{filepath.Join(root, "new"), filepath.Join(alias, "new")} {
		if err := SnapshotSidecar(t.Context(), root, "input", dest, hash); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("source root accepted as staging: %v", err)
		}
		if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("wrote into source root")
		}
	}
}

func TestHonchoTruncatedResponseCleansPartialSnapshot(t *testing.T) {
	api := fixture(t, "openapi-synthetic.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" {
			w.Write(api)
			return
		}
		w.Header().Set("Content-Length", "100")
		w.Write([]byte("partial"))
	}))
	defer srv.Close()
	h, err := OpenHoncho(t.Context(), srv.URL, "/openapi.json", api, secret.New("synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	dest := filepath.Join(t.TempDir(), "failed")
	if err = h.Snapshot(t.Context(), "/workspaces/synthetic/messages", dest); err == nil {
		t.Fatal("truncated response succeeded")
	}
	if _, err = os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial response left behind")
	}
}

func TestSQLiteColumnMetadataGate(t *testing.T) {
	for _, mutate := range []func(*Column){
		func(c *Column) { c.Name = "other" }, func(c *Column) { c.Type = "BLOB" }, func(c *Column) { c.NotNull = false },
		func(c *Column) { s := "'default'"; c.Default = &s }, func(c *Column) { c.PrimaryKey = 1 }, func(c *Column) { c.Hidden = 2 },
	} {
		path, _ := sourceDB(t)
		known := syntheticSchema()
		columns := known.Tables["messages"]
		mutate(&columns[1])
		if err := SnapshotSQLite(t.Context(), path, filepath.Join(t.TempDir(), "snapshot"), known); !errors.Is(err, ErrSchema) {
			t.Fatalf("changed column descriptor accepted: %v", err)
		}
	}
}

func TestHonchoRefusesRemotePlaintextBeforeSendingCredential(t *testing.T) {
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	calls := 0
	transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
		calls++
		return nil, errors.New("unexpected network access")
	}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	for _, address := range []string{"http://example.invalid", "http://example.invalid:8080"} {
		reader, err := OpenHoncho(t.Context(), address, "/openapi.json", fixture(t, "openapi-synthetic.json"), secret.New("synthetic-read-credential"))
		if reader != nil {
			reader.Close()
		}
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("remote plaintext not refused: %v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("plaintext credential request reached transport %d times", calls)
	}
}
