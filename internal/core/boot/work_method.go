// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/exchange"
)

func (r *conversationRuntime) preparedMethod(ctx context.Context, turnID, agent string) (agents.WorkMethodSettings, error) {
	p, err := r.agents.Prepare(ctx, turnID, agent)
	if err != nil {
		return agents.WorkMethodSettings{}, err
	}
	state, err := r.agents.State(ctx, agent)
	if err != nil {
		return agents.WorkMethodSettings{}, err
	}
	return agents.ProjectWorkMethod(p, state), nil
}

func dispatches(m agents.WorkMethodSettings) bool {
	return string(m.Values["workers.dispatch"]) == "true"
}
func ownsCode(m agents.WorkMethodSettings) bool {
	var owns []string
	_ = json.Unmarshal(m.Values["handoff.owns"], &owns)
	return dispatches(m) && slices.Contains(owns, "code")
}

func (r *conversationRuntime) codeOwner(ctx context.Context, turnID, person, preferred string) (string, error) {
	all, err := r.agents.List(ctx)
	if err != nil {
		return "", err
	}
	// Keep the selected agent when it owns code; other choices remain stable.
	slices.SortFunc(all, func(a, b agents.Agent) int {
		if a.ID == b.ID {
			return 0
		}
		if a.ID == preferred {
			return -1
		}
		if b.ID == preferred {
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, a := range all {
		if a.Preset != "dev" {
			continue
		}
		p, err := r.agents.Prepare(ctx, turnID+":code-owner:"+a.ID, a.ID)
		if err != nil {
			return "", err
		}
		if p.OwnerPersonID != person {
			continue
		}
		state, err := r.agents.State(ctx, a.ID)
		if err != nil {
			return "", err
		}
		if ownsCode(agents.ProjectWorkMethod(p, state)) {
			return a.ID, nil
		}
	}
	return "", errDevUnavailable
}

// Delivery receipts make a cross-agent coding assignment visible in the same
// durable exchange log as other handoffs. Retrying uses the original deadline.
func (r *conversationRuntime) codingExchange(ctx context.Context, a codingAssignment) error {
	if a.Origin.Agent == a.Owner {
		return nil
	}
	store := exchange.NewWithMaterials(r.db, r.materials)
	at := time.Now().UTC()
	receipt, err := store.Start(ctx, exchange.Request{DeliveryID: "coding:" + a.ID, From: a.Origin.Agent, To: a.Owner, HandOff: exchange.HandOff{
		Subject: "Work request", Wanted: string(a.Arguments), OutOfScope: "Outside the supplied work definition", Done: "The owner records the work result", Check: "coding:" + a.ID, Deadline: at.Add(24 * time.Hour),
	}}, at)
	if err != nil {
		return err
	}
	_, err = store.Deliver(ctx, receipt.DeliveryID, at)
	return err
}
