// SPDX-License-Identifier: Apache-2.0
package moirai

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/moirai/jobapi"
)

type readyJobs []jobapi.Work

func (r readyJobs) Ready(context.Context, time.Time) ([]jobapi.Work, error) { return r, nil }

type coreAPI struct{ blocked, applied chan struct{} }

func (c coreAPI) Aux(ctx context.Context, r jobapi.AuxRequest) (jobapi.AuxResult, error) {
	if r.JobID == "slow" {
		select {
		case <-c.blocked:
			return jobapi.AuxResult{}, jobapi.ErrUnavailable
		case <-ctx.Done():
			return jobapi.AuxResult{}, ctx.Err()
		}
	}
	return jobapi.AuxResult{Output: []byte(`{"plan":"synthetic"}`)}, nil
}
func (c coreAPI) Apply(_ context.Context, p jobapi.Proposal) (jobapi.Applied, error) {
	close(c.applied)
	return jobapi.Applied{Watermark: p.Through}, nil
}
func (c coreAPI) Report(context.Context, string, jobapi.JobState) error { return nil }
func TestSlowJobDoesNotBlockAnotherJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	core := coreAPI{blocked: make(chan struct{}), applied: make(chan struct{})}
	work := readyJobs{{Epoch: 1, Job: jobapi.Job{ID: "slow", Timeout: time.Second}, Task: jobapi.Task{Sequence: 1}}, {Epoch: 1, Job: jobapi.Job{ID: "ready", Timeout: time.Second}, Task: jobapi.Task{Sequence: 1}}}
	result := make(chan error, 1)
	go func() { result <- (Worker{Reader: work, Core: core}).Once(ctx) }()
	select {
	case <-core.applied:
	case <-ctx.Done():
		t.Fatal("slow job prevented another from applying")
	}
	close(core.blocked)
	if err := <-result; !errors.Is(err, jobapi.ErrUnavailable) {
		t.Fatal(err)
	}
}

type repeatReader struct {
	work     jobapi.Work
	started  <-chan struct{}
	observed chan struct{}
	reads    int
}

func (r *repeatReader) Ready(ctx context.Context, _ time.Time) ([]jobapi.Work, error) {
	r.reads++
	if r.reads > 1 {
		select {
		case <-r.started:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		select {
		case r.observed <- struct{}{}:
		default:
		}
	}
	return []jobapi.Work{r.work}, nil
}

type waitingCore struct {
	started chan struct{}
	calls   atomic.Int64
}

func (c *waitingCore) Aux(ctx context.Context, _ jobapi.AuxRequest) (jobapi.AuxResult, error) {
	if c.calls.Add(1) == 1 {
		close(c.started)
	}
	<-ctx.Done()
	return jobapi.AuxResult{}, ctx.Err()
}
func (*waitingCore) Apply(context.Context, jobapi.Proposal) (jobapi.Applied, error) {
	return jobapi.Applied{}, errors.New("unexpected Apply")
}
func (*waitingCore) Report(context.Context, string, jobapi.JobState) error {
	return errors.New("unexpected Report")
}
func TestDaemonDoesNotDuplicateActiveJobAndJoinsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	core := &waitingCore{started: make(chan struct{})}
	reader := &repeatReader{work: jobapi.Work{Epoch: 1, Job: jobapi.Job{ID: "job", Timeout: time.Minute}, Task: jobapi.Task{Sequence: 1}}, started: core.started, observed: make(chan struct{}, 1)}
	done := make(chan error, 1)
	go func() { done <- (Worker{Reader: reader, Core: core}).Run(ctx, time.Millisecond) }()
	select {
	case <-reader.observed:
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not revisit active work")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("daemon did not join canceled work")
	}
	if core.calls.Load() != 1 {
		t.Fatalf("concurrent calls=%d", core.calls.Load())
	}
}

// Decode persisted Core policy so this regression also exercises an older
// snapshot reader's handling of the deterministic flag before the fix exists.
func TestStepUsesCoreDeterministicPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		deterministic bool
		allowance     int64
	}{
		{"deterministic without model allowance", true, 0},
		{"deterministic with unused allowance", true, 10},
		{"model without allowance", false, 0},
		{"model with allowance", false, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(map[string]any{"ID": "job", "Deterministic": tc.deterministic, "Budget": tc.allowance, "MaxTokens": tc.allowance, "Timeout": time.Second})
			if err != nil {
				t.Fatal(err)
			}
			var job jobapi.Job
			if err := json.Unmarshal(data, &job); err != nil {
				t.Fatal(err)
			}
			api := &routeAPI{allowance: tc.allowance}
			input := json.RawMessage(`{"imported":"synthetic fact"}`)
			err = (Worker{Core: api}).Step(t.Context(), jobapi.Work{Epoch: 1, Job: job, Task: jobapi.Task{Sequence: 1, Input: input}})
			if tc.deterministic {
				if err != nil || api.auxCalls != 0 || len(api.applied) != 1 || string(api.applied[0].Output) != string(input) {
					t.Fatalf("deterministic step: Aux=%d Apply=%v error=%v", api.auxCalls, api.applied, err)
				}
			} else if tc.allowance == 0 {
				if !errors.Is(err, jobapi.ErrBudget) || api.auxCalls != 1 || len(api.applied) != 0 {
					t.Fatalf("model policy bypassed: %+v %v", api, err)
				}
			} else if err != nil || api.auxCalls != 1 || len(api.applied) != 1 || string(api.applied[0].Output) != `{"model":"result"}` {
				t.Fatalf("model route: %+v %v", api, err)
			}
		})
	}
}

type routeAPI struct {
	allowance int64
	auxCalls  int
	applied   []jobapi.Proposal
}

func (a *routeAPI) Aux(context.Context, jobapi.AuxRequest) (jobapi.AuxResult, error) {
	a.auxCalls++
	if a.allowance == 0 {
		return jobapi.AuxResult{}, jobapi.ErrBudget
	}
	return jobapi.AuxResult{Output: json.RawMessage(`{"model":"result"}`)}, nil
}
func (a *routeAPI) Apply(_ context.Context, p jobapi.Proposal) (jobapi.Applied, error) {
	a.applied = append(a.applied, p)
	return jobapi.Applied{Watermark: p.Through}, nil
}
func (*routeAPI) Report(context.Context, string, jobapi.JobState) error { return nil }

type planningResultAPI struct {
	routeAPI
	output json.RawMessage
}

func (a *planningResultAPI) Aux(context.Context, jobapi.AuxRequest) (jobapi.AuxResult, error) {
	return jobapi.AuxResult{Output: a.output}, nil
}

func TestStepRelaysOnlyClosedPlanningRejection(t *testing.T) {
	for _, raw := range []string{jobapi.InvalidPlanResult, `{"rejection":"invalid_plan","other":true}`} {
		api := &planningResultAPI{output: json.RawMessage(raw)}
		work := jobapi.Work{Epoch: 1, Job: jobapi.Job{ID: "planning", Kind: jobapi.Planning, Adoption: "plan-draft-v1", Timeout: time.Second}, Task: jobapi.Task{Sequence: 1, Input: json.RawMessage(`{"kind":"goal"}`)}}
		err := (Worker{Core: api}).Step(t.Context(), work)
		if raw == jobapi.InvalidPlanResult {
			if err != nil || len(api.applied) != 1 || string(api.applied[0].Output) != raw {
				t.Fatalf("completed rejection did not reach Core: %+v %v", api.applied, err)
			}
		} else if !errors.Is(err, jobapi.ErrInvalid) || len(api.applied) != 0 {
			t.Fatalf("malformed rejection reached Apply: %+v %v", api.applied, err)
		}
	}
}
