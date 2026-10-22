// SPDX-License-Identifier: Apache-2.0
package backup

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
)

func TestRecordsGenerationBeforeAndAfterFirstWrite(t *testing.T) {
	cfg, _ := fixture(t)
	repo, err := records.Open(cfg.Root, cfg.Canon)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	check := func(head string) {
		t.Helper()
		m, err := Create(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = Verify(t.Context(), filepath.Join(cfg.Root, "backups", m.GenerationID)); err != nil {
			t.Fatal(err)
		}
		if head == "" {
			if len(m.Repositories) != 0 {
				t.Fatal("invented commit", m.Repositories)
			}
		} else if len(m.Repositories) != 1 || m.Repositories[0] != (Repository{Path: "versions/records", Head: head}) {
			t.Fatal("wrong records head", m.Repositories)
		}
	}
	check("")
	change := records.Change{ID: "stable", Path: "inbox/notes.md", Data: []byte("---\nid: \"stable\"\nkind: memo\n---\n\nNote\n")}
	repo.PublicationHook = func(stage string) error {
		if stage == "lock" {
			return errors.New("interrupted")
		}
		return nil
	}
	if _, err = repo.Commit(t.Context(), change, records.Actor{Person: "person", Agent: "agent"}); err == nil {
		t.Fatal("missing fault")
	}
	check("") // An interrupted first attempt still has no published commit.
	repo.PublicationHook = nil
	head, err := repo.Commit(t.Context(), change, records.Actor{Person: "person", Agent: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	check(head)
}
