// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"time"

	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

// Known remote work has a bounded observer independent of the model turn.
// Recovery mints observation authority only; old answers cannot restart sends.
func (r *conversationRuntime) startA2AObserver(ctx context.Context) {
	watch, cancel := context.WithCancel(ctx)
	r.a2aStop = cancel
	r.a2aDone = make(chan struct{})
	go func() {
		defer close(r.a2aDone)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				r.advanceA2A(watch)
			}
		}
	}()
}
func (r *conversationRuntime) advanceA2A(ctx context.Context) {
	calls, err := r.remoteAgents.Calls(ctx, r.person)
	if err != nil {
		return
	}
	children := map[string]bool{}
	for _, c := range calls {
		if c.Parent != "" {
			children[c.Parent] = true
		}
	}
	for _, call := range calls {
		if ctx.Err() != nil {
			return
		}
		if call.Validated && !call.Delivered {
			_ = r.deliverA2A(ctx, call)
			continue
		}
		if call.TaskID == "" || !time.Now().Before(call.Deadline) {
			continue
		}
		connection, err := r.remoteAgents.Connection(ctx, r.person, call.Connection)
		if err != nil || !connection.Enabled || connection.Revision != call.Revision {
			continue
		}
		args := remoteArgs{Operation: "status", Call: call.ID}
		id := "a2a-observe-" + rand.Text()
		switch call.State {
		case "working":
		case "auth_required":
			if connection.CredentialVersion == call.AuthVersion {
				continue
			}
		case "input_required":
			if children[call.ID] || call.Answer == "" || call.AnswerEpoch != r.epoch {
				continue
			}
			args = remoteArgs{Operation: "send", Connection: call.Connection, Task: call.Profile.Name, Text: call.Answer, Call: call.ID}
			id = "a2a-answer-" + call.ID
		default:
			continue
		}
		view, err := r.router.view(ctx, clientsrv.Session{PersonID: r.person}, call.Origin.Agent)
		if err != nil || view.conversation != call.Origin.Conversation {
			continue
		}
		bindings := map[string]loop.Tool{}
		if _, err = view.prepareA2ATools(call.Origin.Turn, nil, bindings); err != nil {
			continue
		}
		tool, ok := bindings["remote_agent"]
		if !ok {
			continue
		}
		raw, _ := json.Marshal(args)
		_ = tool.Execute(ctx, provider.ToolCall{ID: id, Name: "remote_agent", Arguments: string(raw)})
	}
}
