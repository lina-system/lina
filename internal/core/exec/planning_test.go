// SPDX-License-Identifier: Apache-2.0
package exec_test

import (
	"context"
	"encoding/json"
	coreexec "github.com/thisisjun786/lina/internal/core/exec"
	"github.com/thisisjun786/lina/internal/core/lifecycle"
	"github.com/thisisjun786/lina/kit/tools"
	"testing"
)

type planningGate struct{}

func (planningGate) Begin(_ context.Context, c tools.Call, _ tools.Spec, _ tools.Input) (tools.Ticket, error) {
	return tools.Ticket{Key: c.Key}, nil
}
func (planningGate) Finish(context.Context, tools.Ticket, tools.Result) error { return nil }
func TestPlanningQueueReceiptCarriesCommittedSequence(t *testing.T) {
	registry := tools.NewRegistry(planningGate{}, lifecycle.NewRegistry(nil))
	calls := 0
	_, err := coreexec.RegisterPlanning(registry, func(context.Context, coreexec.PlanDraftRequest) (coreexec.QueuedPlan, error) {
		calls++
		return coreexec.QueuedPlan{JobID: "planning", Sequence: 7}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(coreexec.PlanDraftRequest{RootID: "goal"})
	got, err := registry.Dispatch(t.Context(), tools.Call{Key: "queue", Name: "draft_plan", Args: raw})
	if err != nil || got.State != tools.Applied || got.Revision != "7" || calls != 1 {
		t.Fatal(got, err, calls)
	}
}
