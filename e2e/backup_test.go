// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
)

func TestM1BackupInstalledConversation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux installation")
	}
	data, err := os.ReadFile("m1/backup.json")
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
	// Reproduce the published v14 schema, retaining its completed conversation
	// while removing the later summary, sign-in, growth and job tables.
	legacy, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.ExecContext(h.ctx, `DROP TRIGGER sum_revoke; DROP VIEW sum_affected; DROP TABLE sum_cover; DROP TABLE sum_edge; DROP TABLE sum_node; DROP TABLE sign_in_identities; DROP TABLE sign_in_host; DROP TABLE owner_binding; DROP TABLE agent_growth_steps; DROP TABLE moirai_jobs; DROP TABLE moirai_tasks; DROP TABLE moirai_aux; DROP TABLE moirai_applied; DROP TABLE moirai_reports; DROP TABLE budget_usage; PRAGMA user_version=14;`)
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
