// SPDX-License-Identifier: Apache-2.0
package ledger_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/kit/secret"
)

// Core's composition layer owns both stores. This real-assets adapter exercises
// the boundary without adding a forbidden production import between L3 stores.
type materialStore struct {
	store  *assets.Store
	writer *assets.MaterialWriter
	saves  int
}

func materials(t *testing.T, store *assets.Store) *materialStore {
	t.Helper()
	w, err := assets.OpenMaterialWriter(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	return &materialStore{store: store, writer: w}
}

type materialWrite struct{ *assets.PreparedMaterial }

func (p materialWrite) Insert(ctx context.Context, tx *canon.Tx) (ledger.Material, error) {
	a, err := p.PreparedMaterial.Insert(ctx, tx)
	return ledger.Material{ID: a.ID, Revision: a.Revision}, err
}

func (m *materialStore) Prepare(ctx context.Context, body secrets.Masked) (ledger.MaterialWrite, error) {
	m.saves++
	p, err := m.writer.Prepare(ctx, strings.NewReader(body.Text()), "ledger-body", assets.Actor{PersonID: "owner", AgentID: "agent"})
	if err != nil {
		return nil, err
	}
	return materialWrite{p}, nil
}

func (m *materialStore) Read(ctx context.Context, ref ledger.Material) (string, error) {
	r, err := m.store.OpenRead(ctx, ref.ID, ref.Revision)
	if err != nil {
		return "", err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	return string(data), err
}

func TestLargeBodyUsesMaskedMaterialAndMissingBytesAreIncomplete(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	root := filepath.Dir(cfg.Path)
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := materials(t, a)
	s := ledger.New(db, m)
	in := input("inline")
	in.Body = strings.Repeat("x", ledger.InlineBytes)
	inline := admit(t, s, guard.EntryOwner, in)
	if inline.Material.ID != "" || m.saves != 0 {
		t.Fatal("inline boundary stored a material")
	}
	token := "ghp_" + strings.Repeat("Synthetic", 5)
	in = input("large")
	in.Body = strings.Repeat("a", ledger.InlineBytes) + " " + token + " end"
	e := admit(t, s, guard.EntryOwner, in)
	if e.Material.ID == "" || m.saves != 1 || strings.Contains(e.Body.Text(), token) {
		t.Fatalf("material: %+v", e.Material)
	}
	var bodyNull bool
	if err := db.Read(t.Context(), func(tx *canon.Tx) error {
		return tx.QueryRowContext(t.Context(), "SELECT body IS NULL FROM conv_event WHERE id=?", in.ID).Scan(&bodyNull)
	}); err != nil {
		t.Fatal(err)
	}
	if !bodyNull {
		t.Fatal("large body duplicated inline")
	}
	asset, err := a.Get(t.Context(), e.Material.ID)
	if err != nil || asset.Area != assets.Lina {
		t.Fatalf("asset: %+v %v", asset, err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, asset.Path))
	if err != nil || string(bytes) != e.Body.Text() || strings.Contains(string(bytes), token) {
		t.Fatalf("material bytes: %v", err)
	}
	if err := db.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	db = open(t, cfg)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m = materials(t, a)
	s = ledger.New(db, m)
	got, err := s.Get(t.Context(), in.ID)
	if err != nil || got.Body.Text() != e.Body.Text() || got.Material != e.Material || got.BytesUnavailable {
		t.Fatalf("material readback: %+v %v", got.Material, err)
	}
	if strings.Contains(got.Body.DerivedText(), secret.Placeholder) || strings.Contains(got.Body.DerivedText(), token) {
		t.Fatal("secret span reached derived text")
	}
	for _, corrupt := range []bool{true, false} {
		if corrupt {
			err = os.WriteFile(filepath.Join(root, asset.Path), []byte("truncated"), 0600)
		} else {
			err = os.Remove(filepath.Join(root, asset.Path))
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err = s.Get(t.Context(), in.ID)
		if !errors.Is(err, ledger.ErrBytesUnavailable) || !got.BytesUnavailable || got.ID != in.ID || got.Body.Text() != "" {
			t.Fatalf("missing/corrupt read: %+v %v", got, err)
		}
		if _, err := got.Message(); !errors.Is(err, ledger.ErrBytesUnavailable) {
			t.Fatalf("incomplete became a message: %v", err)
		}
	}
}

func TestExplicitSecretSpansAndNonsecretTags(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	s := ledger.New(db, nil)
	in := input("spans")
	in.Body = "한글 opaque-value third party"
	in.SecretSpans = []secrets.Span{{Start: 7, End: 19}, {Start: 8, End: 18}}
	start := 7 + len(secret.Placeholder) + 1
	in.Tags = []secrets.Span{{Start: 0, End: 6, Sensitivity: "personal_id"}, {Start: start, End: start + 11, Sensitivity: "third_party"}}
	admit(t, s, guard.EntryOwner, in)
	got, err := s.Get(t.Context(), in.ID)
	if err != nil || got.Body.Text() != "한글 "+secret.Placeholder+" third party" || got.Body.DerivedText() != "한글  third party" || len(got.Spans) != 3 {
		t.Fatalf("spans: %+v %v", got, err)
	}
	message, err := got.Message()
	if err != nil || message.Body() != got.Body.DerivedText() {
		t.Fatalf("model projection: %v", err)
	}
	for _, span := range got.Spans {
		if span.Sensitivity == "secret" && (span.Start != 7 || span.End != 7+len(secret.Placeholder)) {
			t.Fatalf("original length leaked: %+v", span)
		}
	}
}

func TestAbsentMaterialWriterFailsClosed(t *testing.T) {
	t.Parallel()
	db, _ := database(t)
	s := ledger.New(db, nil)
	in := input("large")
	in.Body = strings.Repeat("a", ledger.InlineBytes+1)
	if got, err := s.Admit(t.Context(), guard.EntryOwner, in); !errors.Is(err, ledger.ErrBytesUnavailable) || got.ID != "" {
		t.Fatalf("no material writer: %+v %v", got, err)
	}
	if _, err := s.Get(t.Context(), in.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestFailedLargeAdmissionLeavesNoRetainedMaterial(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	a, err := assets.Open(t.Context(), filepath.Dir(cfg.Path), db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	m := materials(t, a)
	failure := errors.New("injected commit failure")
	s := ledger.New(failingCanon{db, failure}, m)
	in := input("failed-large")
	in.Body = strings.Repeat("x", ledger.InlineBytes+1)
	if e, err := s.Admit(t.Context(), guard.EntryOwner, in); !errors.Is(err, failure) || e.ID != "" {
		t.Fatalf("failure: %+v %v", e, err)
	}
	if _, err := s.Get(t.Context(), in.ID); !errors.Is(err, ledger.ErrNotFound) {
		t.Fatal(err)
	}
	manifest, err := a.Backup(t.Context(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 0 {
		t.Fatalf("failed admission retained %d material revisions in backup", len(manifest))
	}
	for _, pattern := range []string{"materials/lina/*/1/content", "staging/material-writes/*"} {
		paths, err := filepath.Glob(filepath.Join(filepath.Dir(cfg.Path), pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("failed admission retained bytes: %v %v", paths, err)
		}
	}
}

func TestDuplicateLargeAdmissionsRetainOnlyOriginalMaterial(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	a, err := assets.Open(t.Context(), filepath.Dir(cfg.Path), db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := ledger.New(db, materials(t, a))
	in := input("large")
	in.Body = strings.Repeat("x", ledger.InlineBytes+1)
	first := admit(t, s, guard.EntryOwner, in)
	in.Body = strings.Repeat("y", ledger.InlineBytes+1)
	for range 3 {
		if e, err := s.Admit(t.Context(), guard.EntryOwner, in); err == nil || e.ID != "" {
			t.Fatalf("duplicate: %+v %v", e, err)
		}
	}
	manifest, err := a.Backup(t.Context(), t.TempDir())
	if err != nil || len(manifest) != 1 || manifest[0].AssetID != first.Material.ID {
		t.Fatalf("retained revisions: %+v %v", manifest, err)
	}
	got, err := s.Get(t.Context(), in.ID)
	if err != nil || got.Body.Text() != first.Body.Text() {
		t.Fatalf("original replaced: %v", err)
	}
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(cfg.Path), "materials/lina/*/1/content"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("retained material files: %v %v", paths, err)
	}
}

func TestKoreanSearchOfMaterialChecksSourceBytes(t *testing.T) {
	t.Parallel()
	db, cfg := database(t)
	root := filepath.Dir(cfg.Path)
	a, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	s := ledger.New(db, materials(t, a))
	in := input("large-search")
	in.Body = strings.Repeat("x", ledger.InlineBytes) + " 도서관에서 공부를 한다."
	e := admit(t, s, guard.EntryOwner, in)
	for _, query := range []string{"도서관", "공부"} {
		hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: query})
		if err != nil || len(hits) != 1 || hits[0].ID != in.ID || hits[0].Body != in.Body {
			t.Fatalf("material search: %v %v", hits, err)
		}
	}
	asset, err := a.Get(t.Context(), e.Material.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, asset.Path)); err != nil {
		t.Fatal(err)
	}
	if hits, err := s.Search(t.Context(), ledger.SearchQuery{AgentID: "agent", PersonID: "owner", Text: "공부"}); !errors.Is(err, ledger.ErrBytesUnavailable) || len(hits) != 0 {
		t.Fatalf("missing source returned cached text: %v %v", hits, err)
	}
}
