// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
)

func TestSQLiteModuleDigestAndInitialization(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestSQLiteModuleProcess$")
	cmd.Env = append(os.Environ(), "LINA_TEST_SQLITE_MODULE=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

// The vector engine is an acceptance input, not a shipped or downloaded test
// dependency. Its construction and exact digest are recorded in the evidence.
func TestSQLiteVectorAcceptance(t *testing.T) {
	path := os.Getenv("LINA_TEST_VECTOR_WASM")
	if path == "" {
		t.Skip("optional vector engine not supplied")
	}
	if os.Getenv("LINA_TEST_VECTOR_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestSQLiteVectorAcceptance$", "-test.v")
		cmd.Env = append(os.Environ(), "LINA_TEST_VECTOR_CHILD=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	if err := UseSQLiteWASM(path, os.Getenv("LINA_TEST_VECTOR_SHA256")); err != nil {
		t.Fatal(err)
	}
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "vector.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if got := textSQL(t, db, "SELECT vec_version()"); got != "v0.1.6" {
		t.Fatal(got)
	}
	execSQL(t, db, "CREATE VIRTUAL TABLE vectors USING vec0(embedding float[2])")
	execSQL(t, db, "INSERT INTO vectors(rowid, embedding) VALUES (1, '[1,2]'), (2, '[4,6]')")
	if got := textSQL(t, db, "SELECT rowid FROM vectors WHERE embedding MATCH '[4,6]' AND k=1"); got != "2" {
		t.Fatal(got)
	}
	if got := textSQL(t, db, "SELECT vec_distance_L2('[1,2]', '[4,6]')"); got != "5" {
		t.Fatal(got)
	}
	// The same engine must retain the required storage behavior.
	TestSQLiteWALFullFTS5AndReopen(t)
	TestSQLiteOnlineBackupReadsLiveWAL(t)
}

func TestSQLiteModuleProcess(t *testing.T) {
	if os.Getenv("LINA_TEST_SQLITE_MODULE") != "1" {
		return
	}
	path := filepath.Join(t.TempDir(), "sqlite.wasm")
	data := append([]byte(nil), sqlite3.Binary...)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "bad", strings.Repeat("0", 64), strings.ToUpper(digest)} {
		if err := UseSQLiteWASM(path, invalid); err == nil {
			t.Fatalf("accepted digest %q", invalid)
		}
	}
	if err := UseSQLiteWASM(path, digest); err != nil {
		t.Fatal(err)
	}
	// Verification and use operate on one byte snapshot, not a path reopened later.
	if err := os.WriteFile(path, []byte("changed after verification"), 0o600); err != nil {
		t.Fatal(err)
	}
	version, got, err := SQLiteEngine()
	if err != nil || version == "" || got != digest {
		t.Fatalf("engine=%s digest=%s error=%v", version, got, err)
	}
	if err := UseSQLiteWASM(path, digest); err == nil {
		t.Fatal("changed engine after initialization")
	}
}
