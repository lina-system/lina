// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/protocol"
)

type scheduledAnswerProbe struct {
	worker.Adapter
	threads []worker.Thread
	answers []worker.Answer
	sent    chan struct{}
}

func (*scheduledAnswerProbe) Name() string { return "codex" }

func (p *scheduledAnswerProbe) Answer(_ context.Context, thread worker.Thread, answer worker.Answer) error {
	p.threads = append(p.threads, thread)
	p.answers = append(p.answers, answer)
	if p.sent != nil {
		p.sent <- struct{}{}
	}
	return nil
}

func TestScheduledWithdrawalKeepsReservationsAndOtherRequests(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	thread := worker.Thread{ID: "thread", TaskID: "task", TurnID: "turn", ProcessGeneration: 1, BindingEpoch: 1}
	scheduler := reactor.NewScheduler(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 1}}, nil
		},
		Start: func(context.Context, work.Card) (work.Run, error) {
			return work.Run{ID: "run", TaskID: thread.TaskID, Generation: 1, Worker: "codex", NodeID: "local", ThreadID: thread.ID, TurnID: thread.TurnID}, nil
		},
	})
	task := work.Task{ID: thread.TaskID, AgentID: r.agent, ConversationID: r.conversation, Title: "task", Done: "checked", Schedule: &work.Schedule{Group: "group", Repository: "owner/repo", Areas: []string{"src/**"}, Worker: "codex", Host: "local"}}
	if err := scheduler.Schedule(t.Context(), []work.Task{task}); err != nil {
		t.Fatal(err)
	}
	if err := scheduler.InputNeeded(t.Context(), thread.TaskID, thread.ID, thread.TurnID); err != nil {
		t.Fatal(err)
	}
	probe := &scheduledAnswerProbe{sent: make(chan struct{}, 1)}
	entry := &workerSession{scheduled: true, origin: r, thread: thread, adapter: probe, questions: map[string]*workerQuestion{},
		answered: func(ctx context.Context, th worker.Thread) {
			if err := scheduler.Answered(ctx, th.TaskID, th.ID, th.TurnID); err != nil {
				t.Error(err)
			}
		},
	}
	for _, id := range []string{"first", "second"} {
		entry.questions[id] = &workerQuestion{requestID: id, event: worker.Event{Thread: thread}, card: protocol.Question{Card: &protocol.DecisionCard{ID: id, Revision: "1"}}}
	}
	addWorkerFixtureSessions(t, r, map[string]*workerSession{thread.ID: entry})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	d := &delegationRuntime{root: r, reactor: scheduler, adapter: &codex.Session{}, done: make(chan struct{}), workerEvents: make(chan worker.Event, 2)}
	go d.events(ctx)
	t.Cleanup(func() { cancel(); <-d.done })
	for _, step := range []struct{ turn, id, state string }{
		{"old-turn", "first", "input_needed"},
		{thread.TurnID, "first", "input_needed"},
		{thread.TurnID, "second", "working"},
	} {
		eventThread := thread
		eventThread.TurnID = step.turn
		d.workerEvents <- worker.Event{Thread: eventThread, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: "server_request_resolved", ItemID: step.id}}
		// A projection response orders the event loop before reading task state.
		d.workerEvents <- worker.Event{Thread: thread, Kind: worker.ToolCallEvent, ToolCall: &worker.ToolCall{ID: "barrier", CallID: "barrier", Namespace: "lina", Name: "read_projection", Arguments: []byte(`{}`)}}
		select {
		case <-probe.sent:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		card, err := r.work.Get(ctx, thread.TaskID)
		if err != nil || card.State != step.state || !card.Active || !card.Scheduling.CapacityHeld || !card.Scheduling.AreaHeld {
			t.Fatalf("withdrawal %s/%s: state=%s active=%v capacity=%v area=%v err=%v", step.turn, step.id, card.State, card.Active, card.Scheduling.CapacityHeld, card.Scheduling.AreaHeld, err)
		}
	}
}

func TestScheduledQuestionAnswersStayBound(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	probe := &scheduledAnswerProbe{}
	resumed := []worker.Thread{}
	newSession := func(id string) *workerSession {
		return &workerSession{scheduled: true, origin: r, adapter: probe,
			thread: worker.Thread{ID: id, TaskID: "task-" + id, AttemptID: "attempt-" + id, TurnID: "turn-" + id, ProcessGeneration: 1, BindingEpoch: 1},
			input:  ledger.Event{Surface: ledger.Surface{Kind: "tui"}}, questions: map[string]*workerQuestion{},
			answered: func(_ context.Context, thread worker.Thread) { resumed = append(resumed, thread) },
		}
	}
	one, other := newSession("one"), newSession("other")
	addWorkerFixtureSessions(t, r, map[string]*workerSession{"one": one, "other": other})
	for _, e := range []*workerSession{one, other} {
		for _, id := range []string{"first", "second"} {
			e.presentQuestion(worker.Event{Thread: e.thread}, id, "input", "Choose a label", []protocol.FormField{{Name: "answer", Type: protocol.FormFieldTypeText}}, nil)
		}
	}
	source := workerQuestions{runtime: r}
	id, revision, value := one.requestKey("first"), "1", "confirmed"
	values := protocol.FormValues{"answer": {String: &value}}
	answer := protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values}
	if _, err := source.AnswerQuestion(t.Context(), "foreign", answer); err == nil {
		t.Fatal("foreign person answered a scheduled worker")
	}
	revision = "stale"
	if outcome, err := source.AnswerQuestion(t.Context(), r.person, answer); err != nil || outcome != protocol.DecisionOutcomeStaleGroup {
		t.Fatalf("stale revision accepted: %s %v", outcome, err)
	}
	if len(probe.answers) != 0 {
		t.Fatal("rejected answer reached a worker")
	}
	revision = "1"
	for range 2 {
		if outcome, err := source.AnswerQuestion(t.Context(), r.person, answer); err != nil || outcome != protocol.DecisionOutcomeResolved {
			t.Fatalf("answer lost its recorded outcome: %s %v", outcome, err)
		}
	}
	if len(probe.answers) != 1 || probe.threads[0] != one.thread || probe.answers[0].RequestID != "first" || len(resumed) != 0 {
		t.Fatalf("duplicate, misrouted or premature answer: %+v %+v %+v", probe.answers, probe.threads, resumed)
	}
	id = one.requestKey("second")
	if _, err := source.AnswerQuestion(t.Context(), r.person, answer); err != nil {
		t.Fatal(err)
	}
	if len(probe.answers) != 2 || probe.threads[1] != one.thread || len(resumed) != 1 || resumed[0] != one.thread {
		t.Fatal("all answered requests did not resume their matching task")
	}
	for _, q := range other.questions {
		if q.card.Card.Outcome != nil {
			t.Fatal("answer affected a different scheduled task")
		}
	}
}
