// SPDX-License-Identifier: Apache-2.0
package hermes

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/kit/secret"
)

func TestSealJSONBodies(t *testing.T) {
	for name, body := range map[string]string{
		"assignment":  `{"token":"synthetic-private-value","public":"visible"}`,
		"escaped":     `{"password":"synthetic-\"private-value","public":"visible"}`,
		"nested":      `{"content":"{\"token\":\"synthetic-private-value\"}","public":"visible"}`,
		"encoded key": `{"to\u006ben":"synthetic-private-value","public":"visible"}`,
		"key block":   `{"content":"-----BEGIN PRIVATE KEY-----\nsynthetic-private-value\n-----END PRIVATE KEY-----","public":"visible"}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, db, key := prepareBodyImport(t, body)
			batches, err := p.PromotionBatches(PromotionPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			for _, batch := range batches {
				if strings.Contains(batch.Quote, "synthetic-private-value") {
					t.Fatal("JSON secret proposed for promotion")
				}
			}
			if _, err := p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
				t.Fatal(err)
			}
			ref, err := Resolve(t.Context(), db, key)
			if err != nil {
				t.Fatal(err)
			}
			event, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid([]byte(event.Body.Text())) || strings.Contains(event.Body.Text(), "synthetic-") || !strings.Contains(event.Body.Text(), "visible") || len(event.Body.Spans()) == 0 {
				t.Fatalf("invalid masked JSON: %q", event.Body.Text())
			}
			if !json.Valid([]byte(event.Body.DerivedText())) || strings.Contains(event.Body.DerivedText(), secret.Placeholder) {
				t.Fatalf("unsafe derived JSON: %q", event.Body.DerivedText())
			}
			assertImportedOriginal(t, p, db, key, body)
		})
	}
}

func TestSealUnmaskableBody(t *testing.T) {
	for name, body := range map[string]string{
		"depth":          strings.Repeat("[", 66) + `"synthetic-private-value"` + strings.Repeat("]", 66),
		"encoding":       "synthetic-private-value\xff",
		"numeric secret": `{"token":123456789}`,
	} {
		t.Run(name, func(t *testing.T) {
			p, db, key := prepareBodyImport(t, body)
			ref, err := Resolve(t.Context(), db, key)
			if err != nil {
				t.Fatal(err)
			}
			batches, err := p.PromotionBatches(PromotionPolicy{})
			if err != nil {
				t.Fatal(err)
			}
			for _, batch := range batches {
				for _, id := range batch.Sources {
					if id == ref.RecordID {
						t.Error("unmaskable body proposed for promotion")
					}
				}
			}
			calls := 0
			for attempt := 0; attempt < 2; attempt++ {
				report, err := p.Seal(t.Context(), db, func(context.Context) (string, error) { calls++; return "generation", nil })
				if err != nil {
					t.Fatal(err)
				}
				// Decode the public report independently of its implementation type.
				encoded, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					UnavailableBodies int
					BodyFailures      []struct{ EventID, Reason string }
				}
				if err := json.Unmarshal(encoded, &got); err != nil {
					t.Fatal(err)
				}
				if got.UnavailableBodies != 1 || len(got.BodyFailures) != 1 || got.BodyFailures[0].EventID != ref.RecordID || got.BodyFailures[0].Reason != "invalid_secret_span" {
					t.Fatalf("missing body failure report: %s", encoded)
				}
				if strings.Contains(string(encoded), "synthetic-private-value") {
					t.Fatal("report leaked source")
				}
			}
			if calls != 1 {
				t.Fatalf("backup calls=%d", calls)
			}
			event, err := ledger.New(db, nil).Get(t.Context(), ref.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(event.Body.Text(), "synthetic-private-value") || !strings.Contains(event.Body.Text(), secret.Placeholder) || !strings.Contains(event.Body.Text(), "invalid_secret_span") || len(event.Body.Spans()) == 0 {
				t.Fatalf("unsafe fallback: %q", event.Body.Text())
			}
			// All other messages and the seal still commit.
			assertCount(t, db, "conv_event", 4)
			assertImportedOriginal(t, p, db, key, body)
		})
	}
}

func TestSealJSONSidecar(t *testing.T) {
	for name, raw := range map[string]string{
		"JSON":           `{"token":"synthetic-private-value","public":"visible"}`,
		"unmaskable":     strings.Repeat("[", 66) + `"synthetic-private-value"` + strings.Repeat("]", 66),
		"numeric secret": `{"password":123456,"public":"visible"}`,
	} {
		t.Run(name, func(t *testing.T) {
			req := importRequest(t)
			db := importStore(t)
			writer, store := importMaterials(t, db)
			req.Materials = writer
			path := filepath.Join(t.TempDir(), "output")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			key := LegacyKey{string(HermesLCM), "profile", "messages", "1"}
			req.Sidecars = []Sidecar{{Key: LegacyKey{string(HermesLCM), "profile", "sidecars", "output"}, Event: key, SnapshotPath: path, SHA256: bytesHash([]byte(raw))}}
			p, err := Prepare(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { p.Close() })
			if _, err = p.Import(t.Context(), db); err != nil {
				t.Fatal(err)
			}
			if _, err = p.Seal(t.Context(), db, func(context.Context) (string, error) { return "generation", nil }); err != nil {
				t.Fatal(err)
			}
			ref, err := Resolve(t.Context(), db, key)
			if err != nil {
				t.Fatal(err)
			}
			events := ledger.New(db, importedMaterials{store})
			event, err := events.Get(t.Context(), ref.RecordID)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(event.Body.Text(), "synthetic-private-value") || len(event.Body.Spans()) == 0 {
				t.Fatalf("unsafe sidecar: %q", event.Body.Text())
			}
			if name == "JSON" && (!json.Valid([]byte(event.Body.Text())) || !strings.Contains(event.Body.Text(), "visible") || event.Material.ID == "") {
				t.Fatalf("damaged JSON sidecar: %+v", event)
			}
			var id string
			var revision int64
			if err = db.Read(t.Context(), func(tx *canon.Tx) error {
				return tx.QueryRowContext(t.Context(), "SELECT material_id,material_revision FROM import_event WHERE id=?", ref.RecordID).Scan(&id, &revision)
			}); err != nil {
				t.Fatal(err)
			}
			original, err := store.Read(t.Context(), id, revision)
			if err != nil || string(original) != raw {
				t.Fatal("sidecar original changed", err)
			}
			hits, err := events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "person", Text: "synthetic-private-value"})
			if err != nil || len(hits) != 0 {
				t.Fatal("sidecar secret reached search", hits, err)
			}
			if name == "JSON" {
				hits, err = events.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "person", Text: "visible"})
				if err != nil || len(hits) != 1 || !json.Valid([]byte(hits[0].Body)) {
					t.Fatalf("lost public JSON search result: %+v %v", hits, err)
				}
			}
			if _, err = p.Verify(t.Context(), db); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func prepareBodyImport(t *testing.T, body string) (*Plan, *canon.Store, LegacyKey) {
	t.Helper()
	req := importRequest(t)
	source, err := canon.OpenSQLite(t.Context(), req.Profiles[0].StatePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.ExecContext(t.Context(), "UPDATE messages SET content=?,role='user' WHERE id=7", body)
	source.Close()
	if err != nil {
		t.Fatal(err)
	}
	p, err := Prepare(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	db := importStore(t)
	if _, err = p.Import(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	return p, db, LegacyKey{string(HermesState), "profile", "messages", "7"}
}

func assertImportedOriginal(t *testing.T, p *Plan, db *canon.Store, key LegacyKey, body string) {
	t.Helper()
	row, err := Reverse(t.Context(), db, key)
	if err != nil || row.text("content") != body {
		t.Fatal("original body changed", err)
	}
	if _, err = p.Verify(t.Context(), db); err != nil {
		t.Fatal(err)
	}
}
