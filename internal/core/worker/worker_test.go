// SPDX-License-Identifier: Apache-2.0
package worker_test

import (
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestEventPayloadAndFailureAreUnambiguous(t *testing.T) {
	thread := worker.Thread{ID: "thread", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	valid := []worker.Event{
		{Thread: thread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Text: "working"}},
		{Thread: thread, Kind: worker.UsageEvent, Usage: &provider.Usage{}},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Done}},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.QuotaFailure}},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted}},
		{Thread: thread, Kind: worker.QuestionEvent, Question: &worker.Request{ID: "1", Method: "question", Params: []byte(`{}`)}},
		{Thread: thread, Kind: worker.ApprovalEvent, Approval: &worker.Request{ID: "2", Method: "approval", Params: []byte(`{}`)}},
		{Thread: thread, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: "3", Name: "receipt", Arguments: []byte(`{}`)}},
	}
	for _, event := range valid {
		if err := event.Validate(); err != nil {
			t.Errorf("%v: %v", event.Kind, err)
		}
	}
	invalid := []worker.Event{
		{},
		{Thread: thread, Kind: worker.EventKind(255)},
		{Thread: thread, Kind: worker.ProgressEvent},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed}},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Done, Failure: worker.QuotaFailure}},
		{Thread: thread, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Done}, Usage: &provider.Usage{}},
		{Thread: thread, Kind: worker.QuestionEvent, Question: &worker.Request{ID: "1", Method: "question", Params: []byte(`broken`)}},
	}
	for i, event := range invalid {
		if event.Validate() == nil {
			t.Errorf("invalid event %d accepted", i)
		}
	}
}

func TestMissingCapabilitiesFailBeforeDispatch(t *testing.T) {
	have := worker.CapabilitySet{worker.StreamProgress, worker.SignalCompletion}
	if err := have.Require(worker.StreamProgress); err != nil {
		t.Fatal(err)
	}
	if have.Require(worker.StreamProgress, worker.WorkTools) == nil {
		t.Fatal("missing work tools accepted")
	}
	if have.Require(worker.Capability("future")) == nil {
		t.Fatal("unknown capability accepted")
	}
}
