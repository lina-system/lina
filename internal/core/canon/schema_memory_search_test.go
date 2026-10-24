// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"context"
	"testing"
)

func TestMemorySearchMigrationMaintainsBothIndexesAndReopens(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	all := Migrations()
	index := -1
	for i, migration := range all {
		if migration.SQL == memorySearchSchema {
			index = i
			break
		}
	}
	if index < 1 {
		t.Fatal("missing appended memory search migration")
	}
	cfg.Migrations = all[:index]
	s := openStore(t, cfg)
	if err := s.Write(t.Context(), func(tx *Tx) error {
		for _, item := range []struct{ id, state, text string }{
			{"kept", "active", "도서관에서 공부를 한다."},
			{"hidden", "retracted", "체육관에서 운동을 한다."},
		} {
			_, err := tx.ExecContext(t.Context(), `INSERT INTO memory_item
			 (id,person,holder,subject_kind,subject_id,normalized,content,scope,state,valid_from,valid_until,supersedes,standing,record)
			 VALUES(?,'person','all','world','',?,?,'local',?,0,0,'',0,'{}')`, item.id, item.text, item.text, item.state)
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Migrations, cfg.Epoch = all, cfg.Epoch+1
	s = openStore(t, cfg)
	check := func(trigram, bigram string, want int) {
		t.Helper()
		if err := s.Read(t.Context(), func(tx *Tx) error {
			var fts, pairs int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search WHERE memory_search MATCH ?", TrigramPhrase(trigram)).Scan(&fts); err != nil {
				return err
			}
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM memory_search_bigram WHERE gram=?", bigram).Scan(&pairs); err != nil {
				return err
			}
			if fts != want || pairs != want {
				t.Fatalf("%q/%q: fts=%d bigram=%d, want %d", trigram, bigram, fts, pairs, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check("도서관", "공부", 1)
	check("체육관", "운동", 0)
	write := func(query string) {
		t.Helper()
		if err := s.Write(t.Context(), func(tx *Tx) error { _, err := tx.ExecContext(t.Context(), query); return err }); err != nil {
			t.Fatal(err)
		}
	}
	write("UPDATE memory_item SET normalized='박물관에서 관람을 한다.', content='박물관에서 관람을 한다.' WHERE id='kept'")
	check("도서관", "공부", 0)
	check("박물관", "관람", 1)
	write("UPDATE memory_item SET state='trashed' WHERE id='kept'")
	check("박물관", "관람", 0)
	write("UPDATE memory_item SET state='active' WHERE id='kept'")
	check("박물관", "관람", 1)
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.Epoch++
	s = openStore(t, cfg)
	check("박물관", "관람", 1)
	check("체육관", "운동", 0)
	write("DELETE FROM memory_item WHERE id='kept'")
	check("박물관", "관람", 0)
	if err := s.Read(t.Context(), func(tx *Tx) error {
		var tokenizer, fallback string
		if err := tx.QueryRowContext(t.Context(), "SELECT tokenizer,fallback FROM search_index_config WHERE name='memory'").Scan(&tokenizer, &fallback); err != nil {
			return err
		}
		if tokenizer != "trigram" || fallback != "hangul_bigram" {
			t.Fatalf("metadata: %q %q", tokenizer, fallback)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
