// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/addons/importer/hermes"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	memoryexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/ledger"
)

// Exercise the installed importer, backup, restart and a real Core memory tool.
// The provider supplies only tool calls; the imported result must come from canon.
func TestM1ImportJSONMasking(t *testing.T) {
	t.Parallel()
	const body = `{"token":"synthetic-private-value","public":"marigold"}`
	var recalled atomic.Bool
	var importedID string // assigned before Core starts making requests
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct{ Input []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, input := range request.Input {
			if strings.Contains(string(input), "synthetic-private-value") {
				t.Error("imported secret reached the provider")
			}
		}
		var last struct{ Type, Output string }
		if len(request.Input) > 0 {
			if err := json.Unmarshal(request.Input[len(request.Input)-1], &last); err != nil {
				t.Error(err)
				return
			}
		}
		if last.Type != "function_call_output" {
			emitPersonaCall(w, "import-recall", "recall_person", map[string]any{"question": "What was imported?", "query": "marigold"})
			return
		}
		var result struct {
			Code   string
			Output memoryexec.PersonAnswer
		}
		if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" || len(result.Output.Sources) != 1 {
			t.Errorf("import recall failed: %s (%v)", last.Output, err)
		} else {
			source := result.Output.Sources[0]
			if source.ID != "event:"+importedID || !json.Valid([]byte(source.Text)) || !strings.Contains(source.Text, "marigold") || strings.Contains(source.Text, "synthetic-private-value") {
				t.Errorf("invalid imported evidence: %+v", source)
			} else {
				recalled.Store(true)
			}
		}
		emitResponse(w, "Imported history is available.", true)
	})))
	defer server.Close()
	h, start := installedConversationHarness(t, server.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 9*operationTimeout)
	defer cancel()
	h.ctx = ctx
	core := start(false)
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
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
	for _, kind := range []string{"lcm", "state"} {
		db, err := canon.OpenSQLite(h.ctx, filepath.Join(source, kind+".db"))
		if err != nil {
			t.Fatal(err)
		}
		ddl, err := os.ReadFile("../../testdata/hermes/" + kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(h.ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
		if kind == "lcm" {
			if _, err = db.ExecContext(h.ctx, "UPDATE host_uid_bindings SET lineage_key='home:synthetic-session'"); err != nil {
				t.Fatal(err)
			}
			_, err = db.ExecContext(h.ctx, "UPDATE messages SET content=? WHERE store_id=1", body)
		} else {
			_, err = db.ExecContext(h.ctx, "UPDATE messages SET content=? WHERE id=1", body)
			if err == nil {
				deep := strings.Repeat("[", 66) + `"synthetic-private-value"` + strings.Repeat("]", 66)
				_, err = db.ExecContext(h.ctx, "INSERT INTO messages(id,session_id,role,content,timestamp) VALUES(7,'synthetic-session','user',?,103)", deep)
			}
		}
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	before := importSourceSnapshot(t, source)
	for attempt := 0; attempt < 2; attempt++ {
		p := h.command("import-hermes", "--state-root", h.root, "--source", source, "--profile", "profile", "--agent", agent, "--person", person, "--owner-session", "synthetic-session", "--confirm-import", "--promote", "none")
		p.start(t)
		p.wait(t, 0)
		decoder := json.NewDecoder(bytes.NewReader(p.stdout.Bytes()))
		var rehearsal json.RawMessage
		if err := decoder.Decode(&rehearsal); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Import hermes.Report
			Seal   hermes.SealReport
		}
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		if result.Seal.UnavailableBodies != 1 || len(result.Seal.BodyFailures) != 1 || result.Seal.BodyFailures[0].Reason != "invalid_secret_span" {
			t.Fatalf("missing body failure report: %+v", result.Seal)
		}
		if attempt == 1 && (result.Import.AddedEvents != 0 || result.Import.AddedReferences != 0 || result.Seal.AddedSeals != 0) {
			t.Fatalf("retry added rows: %+v", result)
		}
		if strings.Contains(p.stdout.String(), "synthetic-private-value") {
			t.Fatal("report leaked source")
		}
		if _, err := backup.Verify(h.ctx, filepath.Join(h.root, "backups", result.Seal.BackupGeneration)); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(before, importSourceSnapshot(t, source)) {
		t.Fatal("import changed the source")
	}
	store, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close(context.Background())
	key := hermes.LegacyKey{System: "hermes-lcm", Instance: "profile", Kind: "messages", Key: "1"}
	ref, err := hermes.Resolve(h.ctx, store, key)
	if err != nil {
		t.Fatal(err)
	}
	importedID = ref.RecordID
	event, err := ledger.New(store, nil).Get(h.ctx, importedID)
	if err != nil || !json.Valid([]byte(event.Body.Text())) || strings.Contains(event.Body.Text(), "synthetic-private-value") || len(event.Body.Spans()) == 0 {
		t.Fatalf("invalid published JSON: %+v %v", event, err)
	}
	row, err := hermes.Reverse(h.ctx, store, key)
	if err != nil || hermes.RowHash(row) != ref.SourceHash {
		t.Fatal("original failed reverse verification", err)
	}
	core = start(true)
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "import-recall", "Find the imported history.")
	if !recalled.Load() {
		t.Fatal("Core did not return imported evidence through recall_person")
	}
	c.Close()
	h.stopCore(core)
}
