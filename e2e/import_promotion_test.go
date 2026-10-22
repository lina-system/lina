// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/backup"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM1ImportRehearsalPromotion(t *testing.T) {
	var fixture struct {
		Profile, Statement string
		OwnerSession       string `json:"owner_session"`
		Expected           int    `json:"expected_items"`
	}
	raw, err := os.ReadFile("m1/import-promotion.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("import called model") }))
	defer provider.Close()
	h, start := installedConversationHarness(t, provider.URL)
	core := start(true)
	c, _ := conversationClient(t, h)
	c.Close()
	h.stopCore(core)
	db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	var agent, person string
	if err = db.QueryRowContext(h.ctx, "SELECT agent_id FROM agent_first_run").Scan(&agent); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRowContext(h.ctx, "SELECT person_id FROM agent_revisions WHERE agent_id=? LIMIT 1", agent).Scan(&person); err != nil {
		t.Fatal(err)
	}
	db.Close()
	source := t.TempDir()
	output := []byte(`{"kind":"tool_result","role":"tool","session_id":"synthetic-session","content":"synthetic large output","content_chars":22,"content_bytes":22,"created_at":103}`)
	if err = os.Mkdir(filepath.Join(source, "lcm-large-outputs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "lcm-large-outputs", "output.json"), output, 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"lcm", "state"} {
		ddl, err := os.ReadFile("../testdata/hermes/" + kind + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		src, err := canon.OpenSQLite(h.ctx, filepath.Join(source, kind+".db"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = src.ExecContext(h.ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
		if kind == "lcm" {
			if _, err = src.Exec(`UPDATE host_uid_bindings SET lineage_key='home:synthetic-session'; INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES(3,'synthetic-session','cli','synthetic-conversation','tool','[Externalized tool output: chars=22; ref=output.json]',103),(4,'synthetic-session','cli','synthetic-conversation','tool','[Externalized tool output: chars=22; ref=missing.json]',104)`); err != nil {
				t.Fatal(err)
			}
		}
		src.Close()
	}
	snapshot := func() map[string]string {
		result := map[string]string{}
		err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			var data []byte
			if !entry.IsDir() {
				data, err = os.ReadFile(path)
				if err != nil {
					return err
				}
			}
			relative, err := filepath.Rel(source, path)
			if err != nil {
				return err
			}
			result[relative] = fmt.Sprintf("%x:%d:%s", sha256.Sum256(data), info.ModTime().UnixNano(), info.Mode())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	before := snapshot()
	args := []string{"import-hermes", "--state-root", h.root, "--source", source, "--profile", fixture.Profile, "--agent", agent, "--person", person, "--owner-session", fixture.OwnerSession}
	run := func(extra ...string) *process {
		p := h.command(append(append([]string{}, args...), extra...)...)
		p.start(t)
		p.wait(t, 0)
		return p
	}
	recall := func(want int) {
		t.Helper()
		core = start(true)
		ready, _ := conversationClient(t, h)
		ready.Close()
		client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "memory", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		op := "list"
		if _, err = client.Memory(h.ctx, protocol.MemoryRequest{Operation: &op, AgentID: &agent, Text: &fixture.Statement}); err != nil {
			t.Fatal(err)
		}
		reply, err := client.Receive(h.ctx)
		if err != nil {
			t.Fatal(err)
		}
		result, ok := reply.Payload.(protocol.MemoryResult)
		if !ok || result.Items == nil || len(*result.Items) != want {
			t.Fatalf("recall: %+v %v", reply, err)
		}
		client.Close()
		h.stopCore(core)
	}
	rehearsal := run("--rehearse")
	var report struct {
		Rehearsal struct{ MissingBytes []string }
	}
	if err = json.Unmarshal(rehearsal.stdout.Bytes(), &report); err != nil || len(report.Rehearsal.MissingBytes) != 1 {
		t.Fatalf("missing output not reported: %+v %v", report, err)
	}
	recall(0)
	// Seal and back up without approving a memory cluster.
	run("--confirm-import", "--promote", "none")
	recall(0)
	run("--confirm-import", "--promote", "all")
	recall(fixture.Expected)
	counts := func() string {
		db, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "canon/canon.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var result string
		for _, table := range []string{"conv_event", "import_record", "legacy_ref", "import_promotion", "memory_item", "memory_revision", "assets", "asset_revisions"} {
			var n int
			if err = db.QueryRowContext(h.ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			result += fmt.Sprint(n, "/")
		}
		return result
	}
	first := counts()
	generations, err := os.ReadDir(filepath.Join(h.root, "backups"))
	generations = slices.DeleteFunc(generations, func(e os.DirEntry) bool { return !e.IsDir() })
	if err != nil || len(generations) != 2 {
		t.Fatalf("seal and promotion backups: %v %v", generations, err)
	}
	found := false
	for _, g := range generations {
		m, err := backup.Verify(h.ctx, filepath.Join(h.root, "backups", g.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if len(m.Assets) != 1 || m.Assets[0].Hash != fmt.Sprintf("%x", sha256.Sum256(output)) {
			t.Fatalf("backup omitted original sidecar: %+v", m.Assets)
		}
		saved, err := canon.OpenSQLiteReader(h.ctx, filepath.Join(h.root, "backups", g.Name(), m.SQLite.Path))
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = saved.QueryRowContext(h.ctx, "SELECT count(*) FROM memory_item").Scan(&n)
		saved.Close()
		if err != nil {
			t.Fatal(err)
		}
		found = found || n == fixture.Expected
	}
	if !found {
		t.Fatal("final generation omitted promotion")
	}
	run("--confirm-import", "--promote", "all")
	if counts() != first {
		t.Fatal("second import added rows")
	}
	again, err := os.ReadDir(filepath.Join(h.root, "backups"))
	again = slices.DeleteFunc(again, func(e os.DirEntry) bool { return !e.IsDir() })
	if err != nil || len(again) != len(generations) {
		t.Fatal("second run added a generation", err)
	}
	recall(fixture.Expected)
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("source changed")
	}
}
