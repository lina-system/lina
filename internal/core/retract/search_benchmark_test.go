// SPDX-License-Identifier: Apache-2.0
package retract_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
)

// BenchmarkForgetIndex reproduces the research note's canon-level probe: a
// removal row, target, memory state and epoch in one transaction. Run with
// -benchtime=1x -count=3 and compare medians; setup is outside the timed region.
func BenchmarkForgetIndex(b *testing.B) {
	for _, count := range []int{400, 800} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			b.StopTimer()
			for run := 0; run < b.N; run++ {
				db, err := canon.OpenSQLite(b.Context(), filepath.Join(b.TempDir(), "probe.sqlite"))
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { db.Close() })
				for _, migration := range canon.Migrations() {
					if _, err := db.ExecContext(b.Context(), migration.SQL); err != nil {
						b.Fatal(err)
					}
				}
				tx, err := db.BeginTx(b.Context(), nil)
				if err != nil {
					b.Fatal(err)
				}
				for i := range count {
					if _, err := tx.ExecContext(b.Context(), searchProbeItemSQL, fmt.Sprint(i), fmt.Sprintf("Synthetic memory %d: 도서관에서 커피를 마신다", i)); err != nil {
						b.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				_, err = db.ExecContext(b.Context(), `BEGIN;
INSERT INTO removal_applied(sequence,id,digest) VALUES(1,'forget','synthetic');
INSERT INTO retract_target(kind,id,person,holder,agent,state,at,provenance,operation_id)
 VALUES('memory','0','person-1','agent-1','agent-1','trashed',1,'{}','forget');
UPDATE memory_item SET state='trashed' WHERE id='0';
UPDATE retract_meta SET epoch=epoch+1 WHERE singleton=1;
COMMIT;`)
				b.StopTimer()
				if err != nil {
					b.Fatal(err)
				}
				if err := db.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
