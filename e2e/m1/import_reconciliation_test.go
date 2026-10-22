// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/memory"
)

// Exercise the installed command, with expectations read directly from synthetic
// sources rather than Plan.Keys, the import report, or stored legacy_ref fields.
func TestM1ImportReconciliation(t *testing.T) {
	var fixture struct {
		Profile            string
		OwnerSession       string `json:"owner_session"`
		Statement          string
		ExpectedReferences int `json:"expected_references"`
		ExpectedEvents     int `json:"expected_events"`
		ExpectedPromoted   int `json:"expected_promoted"`
		Sources            []struct {
			Kind, Setup string
			Keys        map[string][]string
		}
	}
	raw, err := os.ReadFile("import-reconciliation.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	h, start := installedConversationHarness(t, "")
	core := start(false)
	h.stopCore(core)
	path := filepath.Join(h.root, "canon/canon.sqlite")
	db, err := canon.OpenSQLiteReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var agent, person string
	err = db.QueryRowContext(h.ctx, `SELECT f.agent_id,r.person_id FROM agent_first_run f JOIN agents a ON a.id=f.agent_id JOIN agent_revisions r ON r.agent_id=a.id AND r.revision=a.revision`).Scan(&agent, &person)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	wantRows := map[hermes.LegacyKey]hermes.Row{}
	// Include omitted indexes, views and operational tables in reconciliation.
	wantCounts := map[string]int{}
	kept := map[string]bool{}
	for _, src := range fixture.Sources {
		srcDB, err := canon.OpenSQLite(h.ctx, filepath.Join(source, src.Kind+".db"))
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := os.ReadFile("../../testdata/hermes/" + src.Kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = srcDB.ExecContext(h.ctx, string(ddl)+"\n"+src.Setup); err != nil {
			t.Fatal(err)
		}
		names, err := srcDB.QueryContext(h.ctx, `SELECT name FROM sqlite_schema WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		var tables []string
		for names.Next() {
			var name string
			if err = names.Scan(&name); err != nil {
				t.Fatal(err)
			}
			tables = append(tables, name)
		}
		if err = names.Err(); err != nil {
			t.Fatal(err)
		}
		names.Close()
		for _, table := range tables {
			ns := "hermes-" + src.Kind + "/" + table
			var n int
			if err = srcDB.QueryRowContext(h.ctx, "SELECT count(*) FROM "+importSQLName(table)).Scan(&n); err != nil {
				t.Fatal(err)
			}
			wantCounts[ns] = n
			columns, keep := src.Keys[table]
			kept[ns] = keep
			if !keep {
				continue
			}
			for _, row := range importSourceRows(t, h.ctx, srcDB, table) {
				key := row[columns[0]].Data
				if len(columns) > 1 {
					var values []hermes.Value
					for _, column := range columns {
						values = append(values, row[column])
					}
					key = importJSONHash(t, values)
				} else if row[columns[0]].Kind == "text" {
					decoded, err := base64.StdEncoding.DecodeString(key)
					if err != nil {
						t.Fatal(err)
					}
					key = string(decoded)
				}
				qualified := hermes.LegacyKey{System: "hermes-" + src.Kind, Instance: fixture.Profile, Kind: table, Key: key}
				if _, exists := wantRows[qualified]; exists {
					t.Fatalf("duplicate fixture key: %+v", qualified)
				}
				wantRows[qualified] = row
			}
		}
		if err = srcDB.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if len(wantRows) != fixture.ExpectedReferences {
		t.Fatalf("fixture rows=%d want %d", len(wantRows), fixture.ExpectedReferences)
	}
	before := importSourceSnapshot(t, source)
	args := []string{"import-hermes", "--state-root", h.root, "--source", source, "--profile", fixture.Profile, "--agent", agent, "--person", person, "--owner-session", fixture.OwnerSession}
	run := func(code int, extra ...string) *process {
		t.Helper()
		p := h.command(append(append([]string{}, args...), extra...)...)
		p.start(t)
		p.wait(t, code)
		if !reflect.DeepEqual(before, importSourceSnapshot(t, source)) {
			t.Fatal("import changed source bytes, metadata or directory entries")
		}
		return p
	}
	checkCounts := func(tables []hermes.Reconciliation) {
		t.Helper()
		seen := map[string]bool{}
		for _, table := range tables {
			ns := table.System + "/" + table.Table
			n, exists := wantCounts[ns]
			if !exists || seen[ns] || table.Instance != fixture.Profile || table.SnapshotRows != n {
				t.Fatalf("source count mismatch: %+v expected=%d exists=%v duplicate=%v", table, n, exists, seen[ns])
			}
			seen[ns] = true
			if !kept[ns] {
				n = 0
			}
			if table.MappedRows != n || table.References != n || (table.Omitted == "") != kept[ns] {
				t.Fatalf("destination count mismatch: %+v expected=%d", table, n)
			}
		}
		if len(seen) != len(wantCounts) {
			t.Fatalf("reported tables=%d source tables=%d", len(seen), len(wantCounts))
		}
	}
	checkRecall := func(want int) {
		t.Helper()
		store, err := canon.OpenReader(h.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close(context.Background())
		hits, err := memory.New(store).Recall(h.ctx, memory.RecallScope{Agent: agent, Person: person}, memory.Query{Text: fixture.Statement, At: 1000})
		if err != nil || len(hits) != want {
			t.Fatalf("recall=%+v want=%d err=%v", hits, want, err)
		}
		for _, hit := range hits {
			if hit.Basis != memory.Imported {
				t.Fatalf("recall lacks imported evidence: %+v", hit)
			}
		}
	}
	initial := importCanonCounts(t, h.ctx, path)
	var rehearsal struct{ Rehearsal hermes.RehearsalReport }
	if err = json.Unmarshal(run(0, "--rehearse").stdout.Bytes(), &rehearsal); err != nil {
		t.Fatal(err)
	}
	checkCounts(rehearsal.Rehearsal.Tables)
	if !reflect.DeepEqual(initial, importCanonCounts(t, h.ctx, path)) {
		t.Fatal("rehearsal wrote canon rows")
	}
	checkRecall(0)
	for attempt := 0; attempt < 2; attempt++ {
		prior := importCanonCounts(t, h.ctx, path)
		p := run(0, "--confirm-import", "--promote", "none")
		decoder := json.NewDecoder(bytes.NewReader(p.stdout.Bytes()))
		if err = decoder.Decode(&rehearsal); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Import hermes.Report
			Seal   hermes.SealReport
		}
		if err = decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		checkCounts(result.Seal.Verification.Tables)
		if result.Seal.Verification.Rows != len(wantRows) {
			t.Fatalf("verified rows=%d want=%d", result.Seal.Verification.Rows, len(wantRows))
		}
		if attempt == 0 {
			if result.Import.AddedReferences != len(wantRows) || result.Import.AddedEvents != fixture.ExpectedEvents {
				t.Fatalf("initial import: %+v", result.Import)
			}
		} else if result.Import.AddedReferences != 0 || result.Import.AddedRecords != 0 || result.Import.AddedEvents != 0 || result.Seal.AddedSeals != 0 || !reflect.DeepEqual(prior, importCanonCounts(t, h.ctx, path)) {
			t.Fatalf("second import added rows: %+v", result)
		}
		checkImportReferences(t, h.ctx, path, wantRows)
		checkRecall(0)
	}
	// A positive control makes an always-empty recall implementation fail too.
	run(0, "--confirm-import", "--promote", "all")
	checkRecall(fixture.ExpectedPromoted)
	// Change only the synthetic schema. Failure must precede any destination row.
	srcDB, err := canon.OpenSQLite(h.ctx, filepath.Join(source, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = srcDB.ExecContext(h.ctx, "ALTER TABLE messages ADD COLUMN unknown_field TEXT"); err != nil {
		t.Fatal(err)
	}
	if err = srcDB.Close(); err != nil {
		t.Fatal(err)
	}
	before = importSourceSnapshot(t, source)
	prior := importCanonCounts(t, h.ctx, path)
	refused := run(78, "--confirm-import", "--promote", "none")
	if !strings.Contains(refused.stderr.String(), hermes.ErrSchema.Error()) {
		t.Fatalf("expected schema refusal: %s", &refused.stderr)
	}
	if !reflect.DeepEqual(prior, importCanonCounts(t, h.ctx, path)) {
		t.Fatal("unknown schema wrote destination rows")
	}
}

func importSQLName(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func importJSONHash(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

// Preserve all SQLite storage classes in the independent source-side oracle.
func importSourceRows(t *testing.T, ctx context.Context, db *sql.DB, table string) []hermes.Row {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT * FROM "+importSQLName(table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result []hermes.Row
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			t.Fatal(err)
		}
		row := hermes.Row{}
		for i, column := range columns {
			var value hermes.Value
			switch v := values[i].(type) {
			case nil:
				value.Kind = "null"
			case int64:
				value = hermes.Value{Kind: "integer", Data: strconv.FormatInt(v, 10)}
			case float64:
				value = hermes.Value{Kind: "real", Data: strconv.FormatFloat(v, 'g', -1, 64)}
			case string:
				value = hermes.Value{Kind: "text", Data: base64.StdEncoding.EncodeToString([]byte(v))}
			case []byte:
				value = hermes.Value{Kind: "blob", Data: base64.StdEncoding.EncodeToString(v)}
			default:
				t.Fatalf("unexpected source storage class %T", v)
			}
			row[column] = value
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func checkImportReferences(t *testing.T, ctx context.Context, path string, want map[hermes.LegacyKey]hermes.Row) {
	t.Helper()
	store, err := canon.OpenReader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	// Walk destination records too, so extra keys or orphan records cannot hide
	// behind a successful lookup of each expected source key.
	var records []string
	if err = store.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT id FROM import_record")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				return err
			}
			records = append(records, id)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	seen := map[hermes.LegacyKey]int{}
	for _, id := range records {
		refs, err := hermes.References(ctx, store, id)
		if err != nil || len(refs) == 0 {
			t.Fatalf("destination without source references: %s %v", id, err)
		}
		for _, ref := range refs {
			original, exists := want[ref.Key]
			if !exists {
				t.Fatalf("unexpected reference: %+v", ref.Key)
			}
			seen[ref.Key]++
			resolved, err := hermes.Resolve(ctx, store, ref.Key)
			if err != nil || resolved != ref {
				t.Fatalf("forward/reverse reference mismatch: %+v %+v %v", resolved, ref, err)
			}
			reversed, err := hermes.Reverse(ctx, store, ref.Key)
			hash := importJSONHash(t, original)
			if err != nil || !reflect.DeepEqual(reversed, original) || hermes.RowHash(reversed) != hash || ref.SourceHash != hash {
				t.Fatalf("source row/hash mismatch for %+v: %v", ref.Key, err)
			}
		}
	}
	for key := range want {
		if seen[key] != 1 {
			t.Fatalf("source key %+v has %d references, want 1", key, seen[key])
		}
	}
}

func importCanonCounts(t *testing.T, ctx context.Context, path string) map[string]int {
	t.Helper()
	db, err := canon.OpenSQLiteReader(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var table string
		if err = rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, table)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	counts := map[string]int{}
	for _, table := range tables {
		var n int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+importSQLName(table)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		counts[table] = n
	}
	return counts
}

func importSourceSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var raw []byte
		if !entry.IsDir() {
			raw, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[relative] = fmt.Sprintf("%x:%d:%s", sha256.Sum256(raw), info.ModTime().UnixNano(), info.Mode())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
