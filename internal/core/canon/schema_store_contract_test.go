// SPDX-License-Identifier: Apache-2.0
package canon

import "testing"

func TestStoreContractRemovesLegacyIndexes(t *testing.T) {
	t.Parallel()
	cfg := storeConfig(t)
	cfg.Migrations = Migrations()
	db := openStore(t, cfg)
	if err := db.Read(t.Context(), func(tx *Tx) error {
		for _, name := range []string{"ledger_search", "ledger_search_text", "ledger_search_bigram", "ledger_search_pending", "memory_search", "memory_search_text", "memory_search_bigram", "records_fts", "records_korean_fts", "records_korean_bigram"} {
			var n int
			if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name=?", name).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				t.Errorf("legacy index remains: %s", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
