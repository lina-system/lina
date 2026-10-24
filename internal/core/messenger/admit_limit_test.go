// SPDX-License-Identifier: Apache-2.0

package messenger

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// The admission bound is the largest id list effectLookupSQL can bind on the
// linked SQLite build. One more id must fail, or a combined history page
// wedges the cursor.
func TestEffectQueryMatchesTheAdmissionLimit(t *testing.T) {
	raw, err := sqlite3.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if got := raw.Limit(sqlite3.LIMIT_VARIABLE_NUMBER, -1); got != sqliteMaxVariables {
		t.Fatalf("sqlite variable limit = %d, want %d", got, sqliteMaxVariables)
	}

	db, err := driver.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE messenger_effect (
		client_id TEXT, message TEXT, agent TEXT, state TEXT,
		platform TEXT, account TEXT, chat TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := queryEffects(db, MaxAdmitMessages); err != nil {
		t.Fatal(err)
	}
	if err := queryEffects(db, MaxAdmitMessages+1); err == nil || !strings.Contains(err.Error(), "too many SQL variables") {
		t.Fatalf("one id past the limit = %v, want too many SQL variables", err)
	}
}

func queryEffects(db *sql.DB, n int) error {
	args := make([]any, 0, effectQueryFixedParams+effectQueryParamsPerID*n)
	args = append(args, "webex", "account-1", "room-1")
	for range 2 {
		for i := range n {
			args = append(args, fmt.Sprintf("m-%d", i))
		}
	}
	rows, err := db.Query(effectLookupSQL(n), args...)
	if err != nil {
		return err
	}
	return rows.Close()
}
