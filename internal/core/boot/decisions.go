// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Share the already-open authority used by the boot's tools. Opening a second
// authority would recover (stop) its pending approvals in the same epoch.
func (r *conversationRuntime) startDecisions(ctx context.Context) error {
	if r.personaAuthority == nil {
		return nil
	}
	d := &clientsrv.Decisions{Authority: r.personaAuthority, Topics: r.topics, Person: r.person, AgentName: func(id string) string {
		for _, agent := range r.identity.Agents {
			if agent.AgentID == id {
				return agent.Name
			}
		}
		return ""
	}}
	d.Questions = runQuestions{runtime: r}
	r.decisions = d
	r.identity.DecisionPresence = d.Presence
	for kind, operation := range d.Operations() {
		r.identity.Operations[kind] = operation
	}
	r.identity.Topics = r.topics
	if r.identity.TopicCapabilities == nil {
		r.identity.TopicCapabilities = map[string]protocol.Capability{"core": clientsrv.DecisionsCapability}
	}
	r.identity.TopicCapabilities["waiting"] = clientsrv.DecisionsCapability
	r.identity.RefreshTopic = func(ctx context.Context, topic string) error {
		if topic == "waiting" {
			if err := d.PublishApprovals(ctx); err != nil {
				return err
			}
			return r.restoreRunQuestions(ctx)
		}
		return nil
	}
	return d.PublishApprovals(ctx)
}
