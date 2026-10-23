// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/worker/dispatch"
)

// TestM2WorkDefinition (m2/work-definition.json) drives dispatch_work through
// installed Core. It refuses missing done conditions, write areas and
// base-commit design sections, including headings inside code examples, before
// a worker is queued. Valid work receives the full packet and produces a
// committed file, observed PR and durable receipt. Moving the source base
// during manager admission leaves the allocated worktree and packet pinned to
// the validated commit. Worker and GitHub boundaries are synthetic; Core, Git
// and the work ledger run normally.
func TestM2WorkDefinition(t *testing.T) {
	t.Parallel()
	testHeadlessWorkerThroughInstalledCore(t, "../m2/work-definition.json", false)
}

func assertDefinitionRefused(t *testing.T, h *harness, fixture string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		var count int
		if err := tx.QueryRowContext(h.ctx, "SELECT (SELECT count(*) FROM worker_dispatch) + (SELECT count(*) FROM coding_assignments) + (SELECT count(*) FROM work_cards)").Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Errorf("invalid definition created %d dispatch, manager or task records", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"packet", "workdir", "created"} {
		if _, err := os.Stat(filepath.Join(fixture, name)); !os.IsNotExist(err) {
			t.Fatalf("invalid definition reached worker: %s (%v)", name, err)
		}
	}
}

func assertDefinitionPacket(t *testing.T, h *harness, fixture, workdir, base string) {
	t.Helper()
	db, err := canon.OpenReader(h.ctx, filepath.Join(h.root, "canon", "canon.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	id := filepath.Base(workdir)
	var raw []byte
	err = db.Read(h.ctx, func(tx *canon.Tx) error {
		return tx.QueryRowContext(h.ctx, "SELECT record FROM worker_dispatch WHERE id=?", id).Scan(&raw)
	})
	var record dispatch.Record
	if err != nil || json.Unmarshal(raw, &record) != nil {
		t.Fatal("missing dispatch record", err)
	}
	if record.BaseCommit != base {
		t.Fatalf("worker used a moving base: %s want %s", record.BaseCommit, base)
	}
	packet, err := os.ReadFile(filepath.Join(fixture, "packet"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(packet)
	if !strings.Contains(content, id) || !strings.Contains(content, base) {
		t.Fatal("worker packet lost assignment or pinned base")
	}
	start := strings.IndexByte(content, '{')
	if start < 0 {
		t.Fatal("worker packet lost definition")
	}
	var definition dispatch.Definition
	if json.Unmarshal(packet[start:], &definition) != nil || !reflect.DeepEqual(definition, record.TaskDefinition()) {
		t.Fatalf("worker packet changed the admitted definition: %+v", definition)
	}
}
