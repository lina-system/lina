// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/records"
	work "github.com/thisisjun786/lina/internal/core/work/goal"
)

func TestGoalWaitWritesTodoWaitingOn(t *testing.T) {
	t.Parallel()
	r, _, agent, _ := managementFixture(t)
	r.agent = agent
	r.conversation = "main:" + agent
	repo, err := records.Open(r.root, r.db, records.NoSync())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	r.records = repo
	const id = "host-reply"
	raw := []byte("---\nid: \"host-reply\"\nkind: todo\nstate: open\nperson_id: \"person\"\n---\n\n# Confirm the host\n")
	if _, err = repo.Commit(t.Context(), records.Change{ID: id, Path: "inbox/host-reply.md", Data: raw}, records.Actor{Person: r.person, Agent: agent}); err != nil {
		t.Fatal(err)
	}
	w := work.Wait{TodoID: id, Who: "host", Since: "2026-10-20", NextCheck: "2026-10-23", Wake: "mail.changed"}
	if err = r.linkGoalWaitingOn(t.Context(), work.Goal{Person: r.person, Agent: agent}, w); err != nil {
		t.Fatal(err)
	}
	if err = r.linkGoalWaitingOn(t.Context(), work.Goal{Person: r.person, Agent: agent}, w); err != nil {
		t.Fatal(err)
	}
	rec, _, err := repo.Read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	todo, _, _, err := records.ReadTodo(rec.Data)
	if err != nil {
		t.Fatal(err)
	}
	if todo.WaitingOn == nil || todo.WaitingOn.Who != "host" || todo.WaitingOn.Since != "2026-10-20" || todo.WaitingOn.NextCheck != "2026-10-23" {
		t.Fatalf("waiting_on: %+v", todo.WaitingOn)
	}
}
