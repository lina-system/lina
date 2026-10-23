// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"errors"
	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type importedMaterials struct{ store *assets.Store }

func (m importedMaterials) Prepare(context.Context, secrets.Masked) (ledger.MaterialWrite, error) {
	return nil, errors.New("read only")
}
func (m importedMaterials) Read(ctx context.Context, ref ledger.Material) (string, error) {
	b, err := m.store.Read(ctx, ref.ID, ref.Revision)
	return string(b), err
}

func TestReviewSealedMaterialReadsAreMaskedAndReversible(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	db := importStore(t)
	writer, store := importMaterials(t, db)
	req.Materials = writer
	raw := []byte("synthetic large result password=synthetic-private-value")
	path := filepath.Join(t.TempDir(), "output")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// A user event has no tool-call projection, so the event keeps the sidecar
	// as its material. TestImportedMaterialToolCallsKeepLiveProjection covers
	// the assistant-with-calls combination.
	req.Sidecars = []Sidecar{{Key: LegacyKey{string(HermesLCM), "profile", "sidecars", "output"}, Event: LegacyKey{string(HermesLCM), "profile", "messages", "1"}, SnapshotPath: path, SHA256: bytesHash(raw)}}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
		t.Fatal(err)
	}
	ref, err := Resolve(t.Context(), db, req.Sidecars[0].Event)
	if err != nil {
		t.Fatal(err)
	}
	events := ledger.New(db, importedMaterials{store})
	event, err := events.Get(t.Context(), ref.RecordID)
	if err != nil || event.Material.ID == "" || !strings.Contains(event.Body.Text(), "synthetic large result") || strings.Contains(event.Body.Text(), "synthetic-private-value") {
		t.Fatalf("sealed material: %+v %v", event, err)
	}
	original, err := store.Read(t.Context(), event.Material.ID, event.Material.Revision)
	if err != nil || string(original) != string(raw) {
		t.Fatal("original changed", err)
	}
	hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "person", Text: "synthetic-private-value"})
	if err != nil || len(hits) != 0 {
		t.Fatal("secret in search", hits, err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}

func TestReviewDiscoverSidecarsAndRehearseWithoutCanon(t *testing.T) {
	t.Parallel()
	req := importRequest(t)
	source := t.TempDir()
	dir := filepath.Join(source, "lcm-large-outputs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"kind":"tool_result","role":"tool","session_id":"synthetic-session","content":"synthetic output","content_chars":16,"content_bytes":16,"created_at":103}`)
	if err := os.WriteFile(filepath.Join(dir, "output.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	sqlSource(t, req.Profiles[0].LCMPath, `UPDATE messages SET content='[Externalized tool output: chars=16; ref=output.json]' WHERE store_id=2;
 INSERT INTO messages(store_id,session_id,source,conversation_id,role,content,timestamp) VALUES(3,'synthetic-session','cli','synthetic-conversation','tool','[Externalized tool output: chars=16; ref=missing.json]',103)`)
	sidecars, err := DiscoverSidecars(t.Context(), source, "lcm-large-outputs", t.TempDir(), req.Profiles)
	if err != nil || len(sidecars) != 2 {
		t.Fatalf("discovery: %+v %v", sidecars, err)
	}
	req.Sidecars = sidecars
	req.Rehearsal = true
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	report, err := p.Rehearse()
	if err != nil || len(report.MissingBytes) != 1 {
		t.Fatalf("missing sidecar report: %+v %v", report, err)
	}
	if _, err = p.Import(t.Context(), importStore(t)); err == nil {
		t.Fatal("rehearsal-only plan wrote canon")
	}
}
