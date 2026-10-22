// SPDX-License-Identifier: Apache-2.0
package progress

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/work"
)

func TestLanesPageScopeAndCurrentGeneration(t *testing.T) {
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
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(context.Background())
	s := work.New(db)
	for i := range PageSize + 4 {
		id := fmt.Sprintf("task-%03d", i)
		task := work.Task{ID: id, AgentID: "agent", PersonID: "owner", ConversationID: "main:agent", Title: strings.Repeat("title", 500), Done: "report"}
		if i == PageSize+1 {
			task.PersonID = "other"
		}
		if i == PageSize+2 {
			task.AgentID = "deleted"
		}
		if err = s.Create(t.Context(), task); err != nil {
			t.Fatal(err)
		}
		if i == PageSize+3 {
			continue
		}
		run := work.Run{ID: "run-" + id, TaskID: id, Generation: 1, Worker: "Codex", ThreadID: "thread-" + id, TurnID: "turn-" + id, NodeID: "local"}
		if err = s.Start(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err = s.Finish(t.Context(), id, run.ID, "failed"); err != nil {
				t.Fatal(err)
			}
			run.ID = "current-generation"
			run.Generation = 2
			if err = s.Start(t.Context(), run); err != nil {
				t.Fatal(err)
			}
		}
	}
	names := map[string]string{"agent": "LINA"}
	page, err := Lanes(t.Context(), db, "owner", "", names)
	if err != nil {
		t.Fatal(err)
	}
	if len(*page.Lanes) != PageSize || page.NextCursor == nil {
		t.Fatalf("page=%+v", page)
	}
	if (*page.Lanes)[0].LaneID != "current-generation" || (*page.Lanes)[0].Generation != "2" {
		t.Fatal("stale generation exposed")
	}
	for _, lane := range *page.Lanes {
		if len([]rune(lane.Title)) > 512 {
			t.Fatal("unbounded display text")
		}
	}
	next, err := Lanes(t.Context(), db, "owner", *page.NextCursor, names)
	if err != nil {
		t.Fatal(err)
	}
	if len(*next.Lanes) != 1 || next.NextCursor != nil || (*next.Lanes)[0].TaskID != fmt.Sprintf("task-%03d", PageSize) {
		t.Fatalf("page leaked private/deleted/unassigned rows: %+v", next)
	}
}
