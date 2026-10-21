// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestHangulBigrams(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"particle", "도서관에서 공부를 한다", []string{"공부", "관에", "도서", "부를", "서관", "에서", "한다"}},
		{"duplicates", "공부 공부를 공부", []string{"공부", "부를"}},
		{"boundaries", "공 부 공,부 공🙂부 abc", nil},
		{"syllable range", "가힣", []string{"가힣"}},
		{"empty", "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HangulBigrams(tc.text); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("bigrams=%q, want %q", got, tc.want)
			}
		})
	}
}

func TestTrigramPhraseIsLiteral(t *testing.T) {
	db, err := OpenSQLite(t.Context(), filepath.Join(t.TempDir(), "search.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execSQL(t, db, `CREATE VIRTUAL TABLE search USING fts5(body, tokenize='trigram')`)
	for _, body := range []string{"도서관에서 공부한다", `a "quoted" phrase`, "alpha OR beta", "unrelated"} {
		execSQL(t, db, "INSERT INTO search(body) VALUES (?)", body)
	}
	for _, query := range []string{"도서관", `"quoted"`, "alpha OR beta", "unrelated"} {
		if got := textSQL(t, db, "SELECT count(*) FROM search WHERE search MATCH ?", TrigramPhrase(query)); got != "1" {
			t.Fatalf("query %q: count=%s", query, got)
		}
	}
	for _, query := range []string{`" OR unrelated OR "`, "NEAR(alpha beta)", "*", ""} {
		if got := textSQL(t, db, "SELECT count(*) FROM search WHERE search MATCH ?", TrigramPhrase(query)); got != "0" {
			t.Fatalf("query %q interpreted as syntax: count=%s", query, got)
		}
	}
}
