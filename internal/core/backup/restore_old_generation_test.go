// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/memory"
	"github.com/thisisjun786/lina/internal/core/recall"
	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/internal/core/retract"
)

// The fixture builds a generation at the schema the release before the citation
// migration left behind, then restores it with the current release through
// `lina restore`. Every later store step must keep that generation readable:
// the same citations, the same visible recall results and every removal
// applied. See docs/design/filesystem.md, Backup generations.
type oldGeneration struct {
	MainID       string       `json:"main_id"`
	Conversation string       `json:"conversation"`
	Person       string       `json:"person"`
	Agent        string       `json:"agent"`
	Events       []oldEvent   `json:"events"`
	Summaries    []oldSummary `json:"summaries"`
	Memories     []oldMemory  `json:"memories"`
	Forgotten    string       `json:"forgotten_before_generation"`
	Removed      string       `json:"removed_after_generation"`
	Erased       string       `json:"erased_after_generation"`
	Brief        oldBrief     `json:"brief"`
	Records      []oldRecord  `json:"records"`
	Citations    []string     `json:"expected_citations"`
	Recall       []string     `json:"expected_recall"`
	HiddenRecall []string     `json:"hidden_recall_text"`
}

type oldEvent struct {
	ID         string         `json:"id"`
	Speaker    string         `json:"speaker"`
	Body       string         `json:"body"`
	Parent     string         `json:"parent"`
	ReplayOf   string         `json:"replay_of"`
	OccurredAt string         `json:"occurred_at"`
	Origin     map[string]any `json:"origin"`
}

type oldSummary struct {
	ID       string `json:"id"`
	Level    int    `json:"level"`
	Revision int    `json:"revision"`
	Status   string `json:"status"`
	Text     string `json:"text"`
	Covers   []struct {
		Event  string `json:"event"`
		Reason string `json:"reason"`
	} `json:"covers"`
	Edges []string `json:"edges"`
}

type oldMemory struct {
	ID        string        `json:"id"`
	State     string        `json:"state"`
	Holder    string        `json:"holder"`
	Kind      string        `json:"kind"`
	Basis     string        `json:"basis"`
	Content   string        `json:"content"`
	Revisions []oldRevision `json:"revisions"`
}

type oldRevision struct {
	Revision int           `json:"revision"`
	Evidence []oldEvidence `json:"evidence"`
}

type oldEvidence struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Revision  int    `json:"revision"`
	Precision string `json:"precision"`
	Quote     string `json:"quote"`
}

type oldBrief struct {
	Generation int `json:"generation"`
	Sentences  []struct {
		Text      string        `json:"text"`
		Citations []oldEvidence `json:"citations"`
	} `json:"sentences"`
}

type oldRecord struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	State  string `json:"state"`
	Title  string `json:"title"`
	Note   string `json:"note"`
	Source string `json:"source"`
}

func TestRestoreGenerationFromBeforeCitationMigration(t *testing.T) {
	t.Parallel()
	scenario := readOldGeneration(t)
	source := t.TempDir()
	if err := os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	generation := buildOldGeneration(t, source, scenario)
	binary := buildLinaCommand(t)
	target := filepath.Join(t.TempDir(), "target")
	installedManifest, _ := installedRelease()
	installRestoreTarget(t, target, scenario.MainID, installedManifest)

	restore := exec.CommandContext(t.Context(), binary, "restore", "--state-root", target, "--generation", generation)
	output, err := restore.CombinedOutput()
	if err != nil {
		t.Fatalf("lina restore: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Verified restore:") {
		t.Fatalf("restore did not verify the generation: %s", output)
	}
	checkRestoredOldGeneration(t, target, scenario)
}

// preCitationMigrations stops before the migration that adds the citation
// table. A generation taken at that schema is restored through the forward
// chain, which must fill the new shapes from the old ones.
func preCitationMigrations(t *testing.T) []canon.Migration {
	t.Helper()
	all := canon.Migrations()
	for i, migration := range all {
		if strings.Contains(migration.SQL, "CREATE TABLE cite (") {
			if i == 0 {
				t.Fatal("citation migration is the first entry")
			}
			return all[:i]
		}
	}
	t.Fatal("citation migration not found")
	return nil
}

func readOldGeneration(t *testing.T) oldGeneration {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "old-generation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var scenario oldGeneration
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

// installedRelease describes the release that restores the generation. The
// fixture records the same artifacts, so restore compatibility holds while only
// the snapshot schema is older.
func installedRelease() (map[string]any, map[string]string) {
	const version = "0.0.1-alpha.dev"
	commit := strings.Repeat("a", 40)
	binary := func(name, digest string) map[string]any {
		return map[string]any{"name": name, "size": 1, "sha256": strings.Repeat(digest, 64)}
	}
	installed := map[string]any{
		"version":     version,
		"commit":      commit,
		"date":        "2026-10-20",
		"target":      runtime.GOOS + "/" + runtime.GOARCH,
		"goToolchain": "go1.27.3",
		"kitVersion":  1,
		"providers": map[string]any{"chatgpt": map[string]any{
			"adapter": "responses", "retryCount": 0,
			"requestFields": []string{"model", "input", "store", "stream"},
		}},
		"sqlite": map[string]any{
			"module": "github.com/ncruces/go-sqlite3", "version": "v0.30.5",
			"moduleSum":     "h1:6usmTQ6khriL8oWilkAZSJM/AIpAlVL2zFrlcpDldCE=",
			"sqliteVersion": "3.51.2", "wasmSHA256": canon.SQLiteWASMSHA256,
			"execution": "wazero", "sqliteVec": nil,
		},
		"supportedCombinations": []any{},
		"binaries":              []any{binary("lina", "b"), binary("lina-node", "c")},
	}
	artifacts := map[string]string{
		"kit":         "1",
		"sqlite_wasm": canon.SQLiteWASMSHA256,
		"lina":        version + "@" + commit,
		"lina-node":   version + "@" + commit,
	}
	return installed, artifacts
}

func buildOldGeneration(t *testing.T, root string, scenario oldGeneration) string {
	t.Helper()
	ctx := t.Context()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := canon.Open(ctx, canon.Config{
		Path:       filepath.Join(root, "canon/canon.sqlite"),
		Epoch:      4,
		Controls:   controls,
		Migrations: preCitationMigrations(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	seedOldGeneration(t, db, root, scenario)

	removals, err := retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := removals.Forget(ctx, retract.Request{Person: scenario.Person, Targets: []retract.Ref{{Kind: retract.Memory, ID: scenario.Forgotten}}}); err != nil {
		t.Fatal(err)
	}
	if err := removals.Close(); err != nil {
		t.Fatal(err)
	}

	// The generation is captured at the old schema with the artifacts the
	// installed release reports, then later removals extend its sibling list.
	_, artifacts := installedRelease()
	captured, err := Create(ctx, Config{Root: root, Canon: db, Controls: controls, MainID: scenario.MainID, Epoch: 4, Artifacts: artifacts})
	if err != nil {
		t.Fatal(err)
	}

	// Removals recorded after the generation still travel with it as its
	// sibling list and must be replayed over the restored canon.
	removals, err = retract.Open(ctx, retract.Config{Canon: db, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := removals.Forget(ctx, retract.Request{Person: scenario.Person, Targets: []retract.Ref{{Kind: retract.Memory, ID: scenario.Removed}}}); err != nil {
		t.Fatal(err)
	}
	if err := removals.Delete(ctx, retract.Request{Person: scenario.Person, Targets: []retract.Ref{{Kind: retract.Memory, ID: scenario.Erased}}}); err != nil {
		t.Fatal(err)
	}
	if err := removals.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := controls.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "backups", captured.GenerationID)
}

func seedOldGeneration(t *testing.T, db *canon.Store, root string, scenario oldGeneration) {
	t.Helper()
	head := commitFixtureRecords(t, root, scenario)
	if err := db.Write(t.Context(), func(tx *canon.Tx) error {
		at := "2026-09-01T09:30:00Z"
		imported := ""
		for _, event := range scenario.Events {
			origin, err := json.Marshal(event.Origin)
			if err != nil {
				return err
			}
			surface := `{"kind":"tui"}`
			if event.Origin["kind"] == "import" {
				surface, imported = `{"kind":"import"}`, event.ID
			}
			var person, parent, replay any
			if event.Speaker == "person" {
				person = scenario.Person
			}
			if event.Parent != "" {
				parent = event.Parent
			}
			if event.ReplayOf != "" {
				replay = event.ReplayOf
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO conv_event
 (id,conversation_id,parent_id,person_id,agent_id,speaker_kind,surface,occurred_at,recorded_at,time_quality,origin,replay_of,body)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				event.ID, scenario.Conversation, parent, person, scenario.Agent, event.Speaker,
				surface, event.OccurredAt, event.OccurredAt, "observed", string(origin), replay, event.Body); err != nil {
				return err
			}
		}
		if imported != "" {
			// A promoted import: the sealed conversation keeps its batch, record
			// and provenance so the event still resolves through the ledger.
			for _, statement := range []struct {
				sql  string
				args []any
			}{
				{"INSERT INTO import_batch(id,created_at) VALUES('fixture-batch','2026-08-01T09:00:00Z')", nil},
				{"INSERT INTO import_record(id,conversation_id,agent_id,kind,stage,body,flags) VALUES(?,?,?,'conversation',8,'','[]')", []any{scenario.Conversation, scenario.Conversation, scenario.Agent}},
				{"INSERT INTO import_conversation(id,batch_id,manifest_hash,speaker_report,hidden) VALUES(?,'fixture-batch',?,'{}',0)", []any{scenario.Conversation, strings.Repeat("0", 64)}},
				{"INSERT INTO import_seal(conversation_id,event_id) VALUES(?,?)", []any{scenario.Conversation, imported}},
			} {
				if _, err := tx.ExecContext(t.Context(), statement.sql, statement.args...); err != nil {
					return err
				}
			}
		}
		for _, node := range scenario.Summaries {
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO sum_node
 (id,conversation_id,level,model,prompt_version,built_at,start_at,end_at,epoch,status,text,dropped,revision)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				node.ID, scenario.Conversation, node.Level, "fixture-model", "v1", at, at, at, 0, node.Status, node.Text, "", node.Revision); err != nil {
				return err
			}
			for ordinal, cover := range node.Covers {
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO sum_cover(node_id,event_id,ordinal,reason) VALUES(?,?,?,?)", node.ID, cover.Event, ordinal, cover.Reason); err != nil {
					return err
				}
			}
			for ordinal, child := range node.Edges {
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO sum_edge(parent_id,child_id,ordinal) VALUES(?,?,?)", node.ID, child, ordinal); err != nil {
					return err
				}
			}
		}
		for _, item := range scenario.Memories {
			latest := item.Revisions[len(item.Revisions)-1]
			record, err := memoryRecord(scenario, item, latest)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item
 (id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				item.ID, scenario.Person, item.Holder, "world", "", strings.ToLower(item.Content), item.Content,
				"local", item.State, 0, 0, "", 0, record); err != nil {
				return err
			}
			for _, revision := range item.Revisions {
				record, err := memoryRecord(scenario, item, revision)
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(t.Context(), "INSERT INTO memory_revision(item_id,revision,record) VALUES(?,?,?)", item.ID, revision.Revision, record); err != nil {
					return err
				}
			}
		}
		// The brief lived outside the published migration catalog before the
		// citation migration, which adopts an existing table when present.
		statements := []string{
			`CREATE TABLE IF NOT EXISTS memory_person_brief (
 person TEXT NOT NULL, agent TEXT NOT NULL, generation INTEGER NOT NULL,
 record BLOB NOT NULL CHECK(json_valid(record)), PRIMARY KEY(person,agent))`,
			"INSERT INTO memory_person_brief VALUES(?,?,?,?)",
		}
		if _, err := tx.ExecContext(t.Context(), statements[0]); err != nil {
			return err
		}
		citations := make([]map[string]any, 0, len(scenario.Brief.Sentences[0].Citations))
		for _, citation := range scenario.Brief.Sentences[0].Citations {
			citations = append(citations, evidenceJSON(citation))
		}
		brief, err := json.Marshal(map[string]any{"sentences": []any{map[string]any{
			"text": scenario.Brief.Sentences[0].Text, "citations": citations,
		}}})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(t.Context(), statements[1], scenario.Person, scenario.Agent, scenario.Brief.Generation, string(brief)); err != nil {
			return err
		}
		_, err = tx.ExecContext(t.Context(), "UPDATE records_state SET head=? WHERE singleton=1", head)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func memoryRecord(scenario oldGeneration, item oldMemory, revision oldRevision) ([]byte, error) {
	evidence := make([]map[string]any, 0, len(revision.Evidence))
	for _, entry := range revision.Evidence {
		evidence = append(evidence, evidenceJSON(entry))
	}
	return json.Marshal(map[string]any{
		"revision": revision.Revision, "state": item.State, "kind": item.Kind, "content": item.Content,
		"person": scenario.Person, "source_agent": scenario.Agent, "basis": item.Basis,
		"scope": "local", "subject": map[string]any{"kind": "world"}, "holder": item.Holder,
		"evidence": evidence,
	})
}

func evidenceJSON(entry oldEvidence) map[string]any {
	value := map[string]any{"kind": entry.Kind, "id": entry.ID, "precision": entry.Precision}
	if entry.Revision != 0 {
		value["revision"] = entry.Revision
	}
	if entry.Quote != "" {
		value["quote"] = entry.Quote
	}
	return value
}

func recordDocument(scenario oldGeneration, record oldRecord) []byte {
	var document strings.Builder
	document.WriteString("---\n")
	fmt.Fprintf(&document, "id: %q\n", record.ID)
	fmt.Fprintf(&document, "kind: %s\nstate: %s\nperson_id: %q\n", record.Kind, record.State, scenario.Person)
	if record.Source != "" {
		fmt.Fprintf(&document, "source: %q\n", record.Source)
	}
	fmt.Fprintf(&document, "---\n\n# %s\n\n%s\n", record.Title, record.Note)
	return []byte(document.String())
}

// commitFixtureRecords writes the records files the way the records repository
// keeps them: Git objects under versions/records with the published head in
// canon. The files are canon; the index is rebuilt after the restore.
func commitFixtureRecords(t *testing.T, root string, scenario oldGeneration) string {
	t.Helper()
	dir := filepath.Join(root, "versions", "records")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, dir, "init", "-b", "records")
	for _, record := range scenario.Records {
		path := filepath.Join(dir, filepath.FromSlash(record.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, recordDocument(scenario, record), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, dir, "add", "-A")
	runFixtureGit(t, dir, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture")
	return strings.TrimSpace(runFixtureGit(t, dir, "rev-parse", "HEAD"))
}

func runFixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(), "HOME="+t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func buildLinaCommand(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.CommandContext(t.Context(), "go", "build", "-mod=readonly", "-o", binary, "../../../cmd/lina")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build lina: %v\n%s", err, output)
	}
	return binary
}

func installRestoreTarget(t *testing.T, root, mainID string, installed map[string]any) {
	t.Helper()
	for _, dir := range []string{"system/core", "system/bin"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(installed)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{
		"system/core/main.json":    []byte(`{"main_id":"` + mainID + `"}`),
		"system/core/epoch":        []byte("45"),
		"system/bin/manifest.json": data,
	} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func checkRestoredOldGeneration(t *testing.T, root string, scenario oldGeneration) {
	t.Helper()
	ctx := t.Context()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatalf("open restored controls: %v", err)
	}
	t.Cleanup(func() { controls.Close() })
	if err := controls.Acquire(); err != nil {
		t.Fatalf("lock restored root: %v", err)
	}
	epoch, err := controls.Epoch()
	if err != nil {
		t.Fatalf("read restored epoch: %v", err)
	}
	db, err := canon.Open(ctx, canon.Config{
		Path:       filepath.Join(root, "canon/canon.sqlite"),
		Epoch:      epoch,
		Controls:   controls,
		Migrations: canon.Migrations(),
	})
	if err != nil {
		t.Fatalf("open restored canon: %v", err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })

	version, recoveredEpoch, err := canon.Inspect(ctx, filepath.Join(root, "canon/canon.sqlite"))
	if err != nil {
		t.Fatalf("inspect restored canon: %v", err)
	}
	if version != len(canon.Migrations()) || recoveredEpoch != epoch {
		t.Errorf("restored schema=%d epoch=%d, want %d and %d", version, recoveredEpoch, len(canon.Migrations()), epoch)
	}

	// Every removal in force is applied to the restored canon, including the
	// ones recorded after the generation.
	for id, want := range map[string]string{scenario.Forgotten: "trashed", scenario.Removed: "trashed", scenario.Erased: "erased"} {
		var state string
		if err := db.Read(ctx, func(tx *canon.Tx) error {
			return tx.QueryRowContext(ctx, "SELECT state FROM retract_target WHERE kind='memory' AND id=?", id).Scan(&state)
		}); err != nil {
			t.Fatalf("removal of %s: %v", id, err)
		}
		if state != want {
			t.Errorf("removal of %s: state=%q, want %q", id, state, want)
		}
	}
	var erasedItems int
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM memory_item WHERE id=?", scenario.Erased).Scan(&erasedItems)
	}); err != nil {
		t.Fatal(err)
	}
	if erasedItems != 0 {
		t.Errorf("permanently deleted item returned")
	}

	// The migrated citations are in place before any product projection runs.
	var citations []string
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT view_kind||'|'||view_id||'|'||added_revision||'|'||coalesce(dropped_revision,'-')||'|'||ordinal||'|'||target_kind||'|'||target_id||'|'||target_revision||'|'||precision||'|'||role
 FROM cite ORDER BY view_kind,view_id,ordinal,CAST(added_revision AS INTEGER)`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				return err
			}
			citations = append(citations, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(citations)
	expected := append([]string{}, scenario.Citations...)
	sort.Strings(expected)
	if strings.Join(citations, "\n") != strings.Join(expected, "\n") {
		t.Errorf("restored citations\ngot:  %q\nwant: %q", citations, expected)
	}

	// Core's startup projections refill the file-backed records and their
	// citations, then the shared index drains every queued source.
	store, err := records.Open(root, db)
	if err != nil {
		t.Fatalf("rebuild records projection: %v", err)
	}
	defer store.Close()
	drain := recall.New(db, nil)
	for {
		completed, err := drain.DrainBatch(ctx)
		if err != nil {
			t.Fatalf("drain recall index: %v", err)
		}
		if completed == 0 {
			break
		}
	}

	var indexed []string
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT kind||':'||source_id FROM recall_doc ORDER BY kind,source_id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				return err
			}
			indexed = append(indexed, row)
		}
		return rows.Err()
	}); err != nil {
		t.Fatal(err)
	}
	expectedIndex := append([]string{}, scenario.Recall...)
	sort.Strings(expectedIndex)
	if strings.Join(indexed, "\n") != strings.Join(expectedIndex, "\n") {
		t.Errorf("indexed sources\ngot:  %q\nwant: %q", indexed, expectedIndex)
	}
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		var missing, stale, total int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_source s WHERE NOT EXISTS
 (SELECT 1 FROM recall_doc d WHERE d.kind=s.kind AND d.source_id=s.source_id)`).Scan(&missing); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM recall_doc d WHERE NOT EXISTS
 (SELECT 1 FROM recall_source s WHERE s.kind=d.kind AND s.source_id=d.source_id AND s.revision=d.revision)`).Scan(&stale); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_doc").Scan(&total); err != nil {
			return err
		}
		if missing != 0 || stale != 0 || total != len(scenario.Recall) {
			t.Errorf("index documents missing=%d stale=%d total=%d, want 0/0/%d", missing, stale, total, len(scenario.Recall))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, text := range scenario.HiddenRecall {
		if err := db.Read(ctx, func(tx *canon.Tx) error {
			var hits int
			if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM recall_text WHERE recall_text MATCH ?", canon.TrigramPhrase(text)).Scan(&hits); err != nil {
				return err
			}
			if hits != 0 {
				t.Errorf("removed text %q remains searchable %d times", text, hits)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var recordCitations int
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, `SELECT count(*) FROM cite WHERE view_kind='record' AND view_id='rec-note'
 AND target_kind='event' AND target_id='ev-native' AND dropped_revision IS NULL`).Scan(&recordCitations)
	}); err != nil {
		t.Fatal(err)
	}
	if recordCitations != 1 {
		t.Errorf("rebuilt record citations=%d, want 1", recordCitations)
	}
	var erasedCitations int
	if err := db.Read(ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(ctx, "SELECT count(*) FROM cite WHERE view_id=?", scenario.Erased).Scan(&erasedCitations)
	}); err != nil {
		t.Fatal(err)
	}
	if erasedCitations != 0 {
		t.Errorf("erased item kept %d citation rows", erasedCitations)
	}
	checkRestoredQuotes(t, db, scenario)
}

// A quote is a span of its cited event, read through the removal-aware ledger
// reader. An event erased after the generation must not come back through the
// text the old snapshot copied into the item.
func checkRestoredQuotes(t *testing.T, db *canon.Store, scenario oldGeneration) {
	t.Helper()
	ctx := t.Context()
	store := memory.New(db).WithQuotes(ledger.New(db, nil).Citation)
	evidence := []memory.Evidence{
		{Kind: memory.Event, ID: "ev-native", Precision: memory.Exact, Quote: "synthetic violet preference"},
		{Kind: memory.Event, ID: "ev-quoted", Precision: memory.Exact, Quote: "synthetic erased sentence"},
		{Kind: memory.MemoryItem, ID: "mem-ancestor", Precision: memory.NoPrecision},
	}
	scope := memory.RecallScope{Person: scenario.Person, Agent: scenario.Agent}
	resolved, err := store.Quotes(ctx, scope, "memory", "mem-live", 3, evidence)
	if err != nil {
		t.Fatalf("resolve restored quotes: %v", err)
	}
	if len(resolved) != 3 || resolved[0].Quote != "synthetic violet preference" || resolved[0].Status != "" {
		t.Errorf("live quote: %+v", resolved)
	}
	if resolved[1].Quote != "" || resolved[1].Status == "" {
		t.Errorf("erased quote surfaced: %+v", resolved[1])
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic erased sentence") {
		t.Errorf("erased event text reachable through a quote: %s", encoded)
	}
}
