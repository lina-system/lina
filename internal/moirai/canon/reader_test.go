// SPDX-License-Identifier: Apache-2.0
package canon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	corecanon "github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/jobs"
	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

func TestReadOnlySnapshotAndMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "canon.db")
	if r, err := Open(t.Context(), path); err == nil {
		r.Close()
		t.Fatal("created missing canon")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	db, err := corecanon.OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(jobs.Schema + `; CREATE TABLE canon_meta(singleton INTEGER, epoch TEXT);INSERT INTO canon_meta VALUES(1,'7');`); err != nil {
		t.Fatal(err)
	}
	j := jobapi.Job{ID: "memory", Kind: jobapi.Memory, Enabled: true, Watermark: 4}
	data, _ := json.Marshal(j)
	if _, err = db.Exec("INSERT INTO moirai_jobs VALUES (?,?)", j.ID, data); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO moirai_tasks VALUES ('memory',5,'{"source":"next"}')`); err != nil {
		t.Fatal(err)
	}
	r, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	work, err := r.Ready(t.Context(), time.Now())
	if err != nil || len(work) != 1 || work[0].Epoch != 7 || work[0].Job.Watermark != 4 || work[0].Task.Sequence != 5 {
		t.Fatalf("snapshot=%+v %v", work, err)
	}
	if _, err = r.db.Exec("DELETE FROM moirai_jobs"); err == nil {
		t.Fatal("reader wrote canon")
	}
	// Read-only file mode remains effective even if query_only is disabled.
	if _, err = r.db.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.Exec("DELETE FROM moirai_jobs"); err == nil {
		t.Fatal("mode=ro was not enforced")
	}
	j.Enabled = false
	data, _ = json.Marshal(j)
	if _, err = db.Exec("UPDATE moirai_jobs SET record=?", data); err != nil {
		t.Fatal(err)
	}
	work, err = r.Ready(t.Context(), time.Now())
	if err != nil || len(work) != 0 {
		t.Fatalf("disabled: %+v %v", work, err)
	}
	j.Enabled = true
	j.NextRun = time.Now().Add(time.Hour)
	data, _ = json.Marshal(j)
	if _, err = db.Exec("UPDATE moirai_jobs SET record=?", data); err != nil {
		t.Fatal(err)
	}
	work, err = r.Ready(t.Context(), time.Now())
	if err != nil || len(work) != 0 {
		t.Fatalf("schedule: %+v %v", work, err)
	}
}
