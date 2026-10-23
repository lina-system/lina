// SPDX-License-Identifier: Apache-2.0
package records

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
)

func TestKoreanRecordSearchLifecycle(t *testing.T) {
	t.Parallel()
	s, root, db := setup(t)
	makeChange := func(id, person, kind, title, note string) Change {
		return Change{ID: id, Path: "inbox/" + id + ".md", Data: []byte(fmt.Sprintf("---\nid: %q\nperson_id: %q\nkind: %s\nstate: active\n---\n\n# %s\n\n%s\n", id, person, kind, title, note))}
	}
	c := makeChange("b-memo", "person", "memo", "도서관에서 준비", "파란 연필을 챙긴다. 연필을 다시 확인한다.")
	c.Base = commit(t, s, c)
	for _, other := range []Change{
		makeChange("a-foreign", "elsewhere", "memo", "도서관에서", "연필을"),
		makeChange("a-document", "person", "document", "도서관에서", "연필을"),
		makeChange("c-title", "person", "memo", "연필은 준비됨", "준비 완료"),
		makeChange("d-separated", "person", "memo", "연", "필"),
		makeChange("e-separated", "person", "memo", "여행", "연 필 연,필 연a필"),
	} {
		commit(t, s, other)
	}
	check := func(search, after string, limit int, want ...string) {
		t.Helper()
		rows, err := s.Query(t.Context(), Params{Person: "person", Kind: "memo", Search: search, After: after, Limit: limit})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.ID)
			if row.ID == c.ID && row.Revision != c.Base {
				t.Errorf("stale revision: %s want %s", row.Revision, c.Base)
			}
		}
		if !reflect.DeepEqual(ids, want) {
			t.Errorf("search=%q after=%q limit=%d: ids=%v want=%v", search, after, limit, ids, want)
		}
	}
	checkAll := func() {
		t.Helper()
		check("연필을", "", 10, "b-memo")
		check("", "", 10, "b-memo", "c-title", "d-separated", "e-separated")
		check("연필", "", 10, "b-memo", "c-title")
		check("도서관", "", 10, "b-memo")
		check("연필", "", 1, "b-memo")
		check("연필", "b-memo", 1, "c-title")
		check(`연필 OR 도서관`, "", 10)
		check(`연필" OR *`, "", 10)
	}
	checkAll()
	c.Data = makeChange(c.ID, "person", "memo", "도서관에서 준비", "수정: 파란 연필을 챙긴다.").Data
	c.Base = commit(t, s, c)
	checkAll()
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	checkAll()
	reopened, err := Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = reopened
	checkAll()
	c.Data = makeChange(c.ID, "person", "memo", "여행 준비", "지우개를 챙긴다.").Data
	c.Base = commit(t, s, c)
	check("연필", "", 10, "c-title")
	check("도서관", "", 10)
	check("지우개", "", 10, "b-memo")
}

func TestKoreanRecordSearchExistingInstallation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	ctl, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close()
	if err = ctl.Acquire(); err != nil {
		t.Fatal(err)
	}
	cfg := canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()[:32]}
	db, err := canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if db != nil {
			db.Close(context.Background())
		}
	}()
	// Seed the old publication directly: current Store requires the current
	// catalog and must not be used as a historical application binary.
	c := Change{ID: "existing", Path: "inbox/existing.md", Data: []byte("---\nid: \"existing\"\nperson_id: \"person\"\nkind: memo\nstate: active\n---\n\n# 도서관에서\n\n연필을 챙긴다.\n")}
	rev := seedLegacyRecord(t, root, db, c)
	var s *Store
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	if err = db.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = canon.Migrations(), 2
	db, err = canon.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect before records.Open can rebuild anything: the migration itself
	// must index existing text without advancing the published record revision.
	if err = db.Read(t.Context(), func(tx *canon.Tx) error {
		var head, tokenizer, fallback string
		var trigrams, bigrams int
		if err := tx.QueryRowContext(t.Context(), "SELECT head FROM records_state").Scan(&head); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT tokenizer,fallback FROM search_index_config WHERE name='records_korean'").Scan(&tokenizer, &fallback); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_korean_fts WHERE records_korean_fts MATCH ?", canon.TrigramPhrase("도서관")).Scan(&trigrams); err != nil {
			return err
		}
		if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM records_korean_bigram WHERE gram='연필'").Scan(&bigrams); err != nil {
			return err
		}
		if head != rev || tokenizer != "trigram" || fallback != "hangul_bigram" || trigrams != 1 || bigrams != 1 {
			t.Errorf("migration: head=%s tokenizer=%s fallback=%s trigrams=%d bigrams=%d", head, tokenizer, fallback, trigrams, bigrams)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s, err = Open(root, db)
	if err != nil {
		t.Fatal(err)
	}
	for _, search := range []string{"연필", "도서관"} {
		rows, err := s.Query(t.Context(), Params{Person: "person", Kind: "memo", Search: search})
		if err != nil || len(rows) != 1 || rows[0].ID != c.ID || rows[0].Revision != rev {
			t.Errorf("existing installation search=%q: rows=%+v err=%v", search, rows, err)
		}
	}
	record, gotRev, err := s.Read(t.Context(), c.ID)
	if err != nil || string(record.Data) != string(c.Data) || gotRev != rev {
		t.Fatal("upgrade changed canonical record", record, gotRev, err)
	}
}

func TestKoreanRecordSearchLiteralPhrases(t *testing.T) {
	t.Parallel()
	s, _, _ := setup(t)
	for _, fixture := range []struct{ id, title, note string }{
		{"a", "합성 워크숍 메모", "가상의 등대는 청록색이다. 준비물: 종이와 연필."},
		{"b", "연필통", "여행 준비"},
		{"c", "인용 메모", `연필 "파란" OR 등대는`},
		{"d", "Go prototype", "Calibration measurements."},
	} {
		data := []byte(fmt.Sprintf("---\nid: %q\nperson_id: \"person\"\nkind: memo\n---\n\n# %s\n\n%s\n", fixture.id, fixture.title, fixture.note))
		commit(t, s, Change{ID: fixture.id, Path: "inbox/" + fixture.id + ".md", Data: data})
	}
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"등대", []string{"a", "c"}},
		{"연필", []string{"a", "b", "c"}},
		{"연필통", []string{"b"}},
		{`연필 "파란" OR 등대`, []string{"c"}},
		{`"연필"`, nil},
		{"연필 OR 종이", nil},
		{"prototype", []string{"d"}},
		{"go", []string{"d"}},
		{"rotot", nil},
	} {
		rows, err := s.Query(t.Context(), Params{Person: "person", Search: tc.query})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		if !reflect.DeepEqual(ids, tc.want) {
			t.Errorf("query=%q ids=%v want=%v", tc.query, ids, tc.want)
		}
	}
}

func TestRecallProjectionFollowsRecordRevision(t *testing.T) {
	t.Parallel()
	s, _, db := setup(t)
	c := Change{ID: "record", Path: "inbox/record.md", Data: doc("record", "도서관에서 공부")}
	c.Base = commit(t, s, c)
	check := func(term string, want int) {
		t.Helper()
		err := db.Read(t.Context(), func(tx *canon.Tx) error {
			var n int
			if err := tx.QueryRowContext(t.Context(), `SELECT count(*) FROM recall_doc WHERE kind='record' AND source_id='record' AND revision=? AND id IN (SELECT rowid FROM recall_text WHERE recall_text MATCH ?)`, string(c.Base), term).Scan(&n); err != nil {
				return err
			}
			if n != want {
				t.Fatalf("record revision search=%d, want %d", n, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	check("도서관", 1)
	c.Data = doc("record", "새로운 계획")
	c.Base = commit(t, s, c)
	check("도서관", 0)
	check("새로운", 1)
	if err := s.Rebuild(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("새로운", 1)
}
