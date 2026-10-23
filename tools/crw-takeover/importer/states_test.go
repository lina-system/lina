// SPDX-License-Identifier: Apache-2.0
package importer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
)

func TestImportStateMappingsAndRetry(t *testing.T) {
	for _, kind := range []string{"issue", "milestone", "initiative", "project", "receipt"} {
		for _, done := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/done=%t", kind, done), func(t *testing.T) {
				repo, journal := setup(t)
				source, want := "tracker", "active"
				if done {
					want = "done"
				}
				switch kind {
				case "issue", "milestone":
					want = "open"
					if done {
						want = "completed"
					}
				case "initiative":
					want = "draft"
				case "receipt":
					source, want = "crw", "observed"
				}
				data, hash := input(t, fmt.Sprintf(`{"source":%q,"kind":%q,"id":"old","title":"Imported work","done":%t,"deadline":"2026-12-01","done_condition":"Evidence checked","payload":{"outcome":"ready_for_review"}}`, source, kind, done))
				first, err := Import(t.Context(), repo, actor, data, hash, journal)
				if err != nil {
					t.Fatal(err)
				}
				id := first.Rows[0].LINAID
				r, rev, err := repo.Read(t.Context(), id)
				if err != nil || !bytes.Contains(r.Data, []byte("\nstate: "+want+"\n")) {
					t.Fatalf("saved state: %s, error: %v", r.Data, err)
				}
				row, err := repo.ViewRecord(t.Context(), actor.Person, id)
				if err != nil || row.State != want {
					t.Fatalf("Core state: %+v, error: %v", row, err)
				}
				if kind == "issue" || kind == "milestone" {
					if row.Todo == nil || row.Todo.State != want || row.Todo.DeadlineSource != "snapshot:"+hash+":tracker:"+kind+":old" {
						t.Fatalf("Core planning fields: %+v", row.Todo)
					}
				}
				head, err := repo.Head(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				second, err := Import(t.Context(), repo, actor, data, hash, journal)
				if err != nil || !second.Rows[0].Reused || second.Rows[0].LINAID != id {
					t.Fatal(second, err)
				}
				after, afterRev, err := repo.Read(t.Context(), id)
				if err != nil || afterRev != rev || !bytes.Equal(after.Data, r.Data) {
					t.Fatal("retry changed record", err)
				}
				afterHead, err := repo.Head(t.Context())
				if err != nil || afterHead != head {
					t.Fatal("retry published another commit", err)
				}
			})
		}
	}
}

func TestLegacyTodoImportRetry(t *testing.T) {
	for _, kind := range []string{"issue", "milestone"} {
		for _, done := range []bool{false, true} {
			for _, mode := range []string{"pending", "acknowledged", "edited-acknowledged", "edited-pending"} {
				t.Run(fmt.Sprintf("%s/done=%t/%s", kind, done, mode), func(t *testing.T) {
					repo, journalPath := setup(t)
					data, hash := input(t, fmt.Sprintf(`{"source":"tracker","kind":%q,"id":"old","title":"Imported work","done":%t,"deadline":"2026-12-01","done_condition":"Evidence checked"}`, kind, done))
					snapshot, err := verified(data, hash)
					if err != nil {
						t.Fatal(err)
					}
					source, _, _ := classify(snapshot.Records[0])
					state, want := "active", "open"
					if done {
						state, want = "done", "completed"
					}
					id := records.NewID()
					// Build the old published bytes independently of today's converter.
					var observation bytes.Buffer
					if err := json.Indent(&observation, snapshot.Records[0], "", "  "); err != nil {
						t.Fatal(err)
					}
					legacy := fmt.Sprintf("---\nid: %q\nkind: todo\nstate: %s\nperson_id: %q\nagent_id: %q\ndone_condition: \"Evidence checked\"\ndeadline: \"2026-12-01\"\nprovenance: {\"source\":\"tracker\",\"kind\":%q,\"id\":\"old\",\"snapshot_sha256\":%q}\n---\n\n# Imported work\n\n\n\n## Source observation\n\n    %s\n", id, state, actor.Person, actor.Agent, kind, hash, strings.ReplaceAll(observation.String(), "\n", "\n    "))
					change := records.Change{ID: id, Path: "projects/imported-work-takeover-1.md", Data: []byte(legacy)}
					rev, err := repo.Commit(t.Context(), change, actor)
					if err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(mode, "edited-") {
						change.Base = rev
						if mode == "edited-acknowledged" {
							change.Data = bytes.Replace(change.Data, []byte("state: "+state), []byte("state: started"), 1)
							// A normal edit saves canonical fields while retaining provenance.
							change.Data = bytes.Replace(change.Data, []byte("\nprovenance:"), []byte("\ndeadline_source: "+quote("snapshot:"+hash+":tracker:"+kind+":old")+"\nprovenance:"), 1)
							want = "started"
						}
						change.Data = append(change.Data, []byte("\nA later user decision.\n")...)
						rev, err = repo.Commit(t.Context(), change, actor)
						if err != nil {
							t.Fatal(err)
						}
					}
					j := journal{Version: 1, SHA256: hash, Actor: actor, Entries: map[string]reservation{source.Ref.key(): {ID: id, Published: strings.HasSuffix(mode, "acknowledged")}}}
					if err = SaveJSON(journalPath, j); err != nil {
						t.Fatal(err)
					}
					head, err := repo.Head(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					for range 2 {
						report, err := Import(t.Context(), repo, actor, data, hash, journalPath)
						if mode == "edited-pending" {
							if err == nil || report.Rows[0].Outcome != "lost" || report.Rows[0].Reason != "reserved LINA id has different content" {
								t.Fatalf("unacknowledged edit accepted: %+v %v", report, err)
							}
						} else if err != nil || !report.Rows[0].Reused || report.Rows[0].LINAID != id {
							t.Fatalf("legacy retry: %+v %v", report, err)
						}
						r, gotRev, err := repo.Read(t.Context(), id)
						if err != nil || gotRev != rev || r.Path != change.Path || !bytes.Equal(r.Data, change.Data) {
							t.Fatal("retry changed saved record", err)
						}
						after, err := repo.Head(t.Context())
						if err != nil || after != head {
							t.Fatal("retry created a commit", err)
						}
					}
					if err := repo.Rebuild(t.Context()); err != nil {
						t.Fatal(err)
					}
					row, err := repo.ViewRecord(t.Context(), actor.Person, id)
					if err != nil || row.State != want || row.Revision != rev || row.Todo == nil || row.Todo.DeadlineSource != "snapshot:"+hash+":tracker:"+kind+":old" {
						t.Fatalf("legacy projection: %+v %v", row, err)
					}
				})
			}
		}
	}
}
