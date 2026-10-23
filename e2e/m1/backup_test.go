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
	// Reproduce the published v14 schema, retaining its completed conversation
	// while removing later summary, sign-in, channel, exchange, display-order, growth, job, skill maintenance, run, work (including completion signals, engine records and events, switch reservations and history), records (including Korean search and to-do views), work-waiting, waiting-decision, merge-authority and coding-delegation tables.
	legacy, err := canon.OpenSQLite(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.ExecContext(h.ctx, `DROP TABLE work_worker_reservations; DROP TABLE work_switch_events; DROP TABLE work_switch_tasks; DROP TABLE work_switches; DROP TABLE timer_fire; DROP TABLE timer; DROP TABLE records_todo; DROP TABLE work_completion_origins; DROP TABLE work_completion_claims; DROP TABLE work_completions; DROP TABLE skill_maintenance_members; DROP TABLE skill_maintenance_notices; DROP TABLE skill_maintenance; DROP TABLE work_records; DROP TABLE work_events; DROP TABLE waiting_decisions; DROP TABLE coding_assignments; DROP TABLE coding_managers; DROP TABLE authority_delegations; DROP TRIGGER records_korean_insert; DROP TRIGGER records_korean_delete; DROP TABLE records_korean_bigram; DROP TABLE records_korean_fts; DELETE FROM search_index_config WHERE name='records_korean'; DROP TABLE channel_steering; DROP TABLE discord_buttons; DROP TABLE discord_presentations; DROP TABLE worker_dispatch; DROP TABLE authority_merges; DROP TABLE work_waiting_decisions; DROP TABLE work_waiting_rounds; DROP TABLE records_edges; DROP TABLE records_text; DROP TABLE records_fts; DROP TABLE records_index_state; DROP TABLE records_index; DROP TABLE records_state; DROP TABLE work_active_runs; DROP TABLE work_cards; DROP TABLE work_ledger; DROP TABLE background_runs; DROP TABLE running_seen; DROP TABLE lane_notices; DROP TABLE lane_notice_events; DROP TABLE conversation_order; DROP TRIGGER import_range_closed; DROP TABLE import_rehearsal; DROP TABLE import_promotion; DROP TABLE import_seal; DROP TABLE import_conversation; DROP TABLE import_link; DROP TABLE import_source_span; DROP TABLE legacy_ref; DROP TABLE import_record; DROP TABLE import_event_span; DROP TABLE import_event_relation; DROP TABLE import_event; DROP TABLE import_batch; DROP TABLE exch_waiting; DROP TABLE exch_event; DROP TABLE exch_msg; DROP TABLE exchanges; DROP TABLE telegram_presentations; DROP TABLE telegram_buttons; DROP TABLE channel_outbound; DROP TABLE channel_receipts; DROP TABLE channel_actions; DROP TABLE channel_bindings; DROP TABLE channel_connections; DROP TRIGGER sum_revoke; DROP VIEW sum_affected; DROP TABLE sum_cover; DROP TABLE sum_edge; DROP TABLE sum_node; DROP TABLE sign_in_identities; DROP TABLE sign_in_host; DROP TABLE owner_binding; DROP TABLE agent_growth_steps; DROP TABLE moirai_jobs; DROP TABLE moirai_tasks; DROP TABLE moirai_aux; DROP TABLE moirai_applied; DROP TABLE moirai_reports; DROP TABLE budget_usage; DROP TABLE skill_heads; DROP TABLE skill_versions; DROP TABLE skill_undone; DROP TABLE skill_repo; PRAGMA user_version=14;`)

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
