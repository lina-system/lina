// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestSwitchOwnerAdmission(t *testing.T) {
	r, session, agent, _ := managementFixture(t)
	r.work = work.New(r.db)
	task := work.Task{ID: "task", PersonID: session.PersonID, AgentID: agent, ConversationID: "conversation", Title: "Report", Done: "Verified", ExternalControl: true}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	tasks := []string{task.ID}
	snapshot := strings.Repeat("a", 64)
	request := protocol.WorkRequest{Operation: "approve_switch", SwitchID: "switch", Tasks: &tasks, Snapshot: &snapshot}
	if _, err := r.Work(t.Context(), session, request, "approval"); err == nil {
		t.Fatal("worker approved switch")
	}
	session.CanDecide = true
	foreign := session
	foreign.PersonID = "other"
	if _, err := r.Work(t.Context(), foreign, request, "approval"); err == nil {
		t.Fatal("foreign person approved switch")
	}
	v, err := r.Work(t.Context(), session, request, "approval")
	if err != nil || v.State != "approved" || v.Approval != "approval" {
		t.Fatalf("approval: %+v %v", v, err)
	}
	if _, err = r.Work(t.Context(), session, request, "different-message"); err == nil {
		t.Fatal("approval was overwritten")
	}
}
