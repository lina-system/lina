// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestM1BackupInstalledConversation(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Skip("Linux installation")
	}
	data, err := os.ReadFile("backup.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Input, Reply  string
		SchemaVersion int `json:"schema_version"`
	}
	if err = json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	// Compile the inspection helper's SQLite engine before the bounded product
	// scenario. Core runs in a separate process and still starts cold.
	if _, _, err := canon.SQLiteEngine(); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { emitResponse(w, scenario.Reply, true) }))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	client, conversation := conversationClient(t, h)
	admittedTurn(t, h, client, conversation, "backup-input", scenario.Input)
	client.Close()
	before, err := os.ReadDir(filepath.Join(h.root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	running := h.command("backup", "--state-root", h.root)
	running.start(t)
	running.wait(t, 69)
	after, err := os.ReadDir(filepath.Join(h.root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("running Core backup wrote state")
	}
	for i := range before {
		if before[i].Name() != after[i].Name() {
			t.Fatal("running Core backup changed backup entries")
		}
	}
	h.stopCore(core)
	// Keep the completed conversation while removing schema objects introduced
	// after the published v14 fixture, using the repository's migration catalog.
	legacy, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	reference, err := canon.OpenSQLite(h.ctx, filepath.Join(t.TempDir(), "v14.sqlite"))
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	defer reference.Close()
	for _, migration := range canon.Migrations()[:14] {
		if _, err = reference.ExecContext(h.ctx, migration.SQL); err != nil {
			legacy.Close()
			t.Fatal(err)
		}
	}
	rows, err := legacy.QueryContext(h.ctx, `SELECT type,name FROM sqlite_schema
WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'
ORDER BY CASE type WHEN 'trigger' THEN 0 WHEN 'view' THEN 1 WHEN 'index' THEN 2 ELSE 3 END,name`)
	if err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	var removals []string
	for rows.Next() {
		var kind, name string
		var retained int
		if err = rows.Scan(&kind, &name); err == nil {
			err = reference.QueryRowContext(h.ctx, "SELECT count(*) FROM sqlite_schema WHERE type=? AND name=?", kind, name).Scan(&retained)
		}
		if err != nil {
			rows.Close()
			legacy.Close()
			t.Fatal(err)
		}
		// Canon creates its epoch fence outside the application migrations.
		if retained == 0 && name != "canon_meta" {
			removals = append(removals, "DROP "+kind+" IF EXISTS \""+strings.ReplaceAll(name, "\"", "\"\"")+"\";")
		}
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		_, err = legacy.ExecContext(h.ctx, strings.Join(removals, "\n")+"\nPRAGMA user_version=14;")
	}
	if err == nil {
		// Later search migrations also insert catalog rows in this old table.
		// Restore the published metadata so their upgrade can run again.
		_, err = legacy.ExecContext(h.ctx, "DELETE FROM search_index_config")
	}
	if err == nil {
		rows, err = reference.QueryContext(h.ctx, "SELECT name,tokenizer,fallback FROM search_index_config")
		if err == nil {
			for rows.Next() {
				var name, tokenizer, fallback string
				if err = rows.Scan(&name, &tokenizer, &fallback); err == nil {
					_, err = legacy.ExecContext(h.ctx, "INSERT INTO search_index_config VALUES(?,?,?)", name, tokenizer, fallback)
				}
				if err != nil {
					break
				}
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
		}
	}
	legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	help := h.command("backup")
	help.start(t)
	help.wait(t, 64)
	p := h.command("backup", "--state-root", h.root)
	p.start(t)
	p.wait(t, 0)
	entries, err := os.ReadDir(filepath.Join(h.root, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(h.root, "backups", entry.Name())
		m, err := backup.Verify(h.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if m.SchemaVersion != scenario.SchemaVersion || m.MainID == "" || m.Epoch == "0" || len(m.Repositories) == 0 {
			t.Fatalf("incomplete installed backup: %+v", m)
		}
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(path, m.SQLite.Path))
		if err != nil {
			t.Fatal(err)
		}
		var events int
		err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM conv_event WHERE conversation_id=?", conversation).Scan(&events)
		db.Close()
		if err != nil || events < 2 {
			t.Fatalf("conversation missing: %d %v", events, err)
		}
		if _, err := backup.Verify(h.ctx, path); err != nil {
			t.Fatalf("reading snapshot mutated generation: %v", err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("generations=%d", count)
	}
	core = start(true)
	restored, restoredConversation := conversationClient(t, h)
	if restoredConversation != conversation {
		t.Fatal("catalog upgrade changed conversation")
	}
	restored.Close()
	h.stopCore(core)
}
