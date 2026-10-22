// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
	"testing"
)

func TestProgressOverviewWiredToCore(t *testing.T) {
	r := controlRuntime(t)
	if _, ok := r.identity.TopicCapabilities["progress"]; !ok {
		t.Fatal("progress capability missing")
	}
	task := work.Task{ID: "hidden-task", PersonID: r.person, AgentID: r.agent, ConversationID: r.conversation, Title: "Prepare workshop", Done: "materials verified"}
	if err := r.work.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	if err := r.work.Start(t.Context(), work.Run{ID: "hidden-run", TaskID: task.ID, Generation: 1, Worker: "Codex", ThreadID: "hidden-thread", TurnID: "hidden-turn", NodeID: r.main}); err != nil {
		t.Fatal(err)
	}
	mode := "overview"
	result, err := r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: r.person}, protocol.Envelope{Payload: protocol.ProgressGet{View: &mode}})
	if err != nil {
		t.Fatal(err)
	}
	p := result.Payload.(protocol.Progress)
	if p.Rows == nil || len(*p.Rows) != 1 || (*p.Rows)[0].Line.Name != task.Title || (*p.Rows)[0].Line.Stage != "unknown" || (*p.Rows)[0].Line.Percent != nil {
		t.Fatalf("unobserved progress: %+v", p)
	}
	if _, err = r.identity.Operations[protocol.KindProgressGet].Handle(t.Context(), clientsrv.Session{PersonID: "stranger"}, protocol.Envelope{Payload: protocol.ProgressGet{View: &mode}}); err == nil {
		t.Fatal("unauthorized progress")
	}
}
