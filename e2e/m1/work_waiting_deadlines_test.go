// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/waiting"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/protocol"
)

// #34's harness starts the real Core; only work executors and the outward
// plugin are stubs. A past, fixed deadline makes recovery deterministic.
func TestM2WorkWaitingDeadlines(t *testing.T) {
	t.Parallel()

	var script struct {
		Deadline    time.Time
		Goals       []string
		Questions   []waiting.Question
		StoppedGoal string `json:"stopped_goal"`
	}
	raw, err := os.ReadFile("../m2/work-waiting-deadlines.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &script); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, buildLina(t))
	core := h.startCore()
	h.client(core)
	waitCoreReady(t, h)
	h.stopCore(core)
	path := filepath.Join(h.root, "canon", "canon.sqlite")
	openWriter := func() (*canon.Store, func()) {
		ctl, err := ctlfiles.Open(h.root)
		if err != nil {
			t.Fatal(err)
		}
		if err = ctl.Acquire(); err != nil {
			t.Fatal(err)
		}
		_, epoch, err := canon.Inspect(h.ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		db, err := canon.Open(h.ctx, canon.Config{Path: path, Epoch: epoch, Controls: ctl, Migrations: canon.Migrations()})
		if err != nil {
			t.Fatal(err)
		}
		close := func() {
			if err := db.Close(context.Background()); err != nil {
				t.Error(err)
			}
			if err := ctl.Close(); err != nil {
				t.Error(err)
			}
		}
		t.Cleanup(close)
		return db, close
	}
	db, closeWriter := openWriter()
	s := work.New(db)
	rounds := waiting.NewWorkRounds(db, s)
	start := func(s *work.Service, id string) error {
		return s.Start(h.ctx, work.Run{ID: "stub:" + id, TaskID: id, Generation: 1, Worker: "stub-worker", ThreadID: "thread:" + id, TurnID: "turn", NodeID: "stub-node"})
	}
	allGoals := append(append([]string{}, script.Goals...), script.StoppedGoal)
	for _, goal := range allGoals {
		task := work.Task{ID: goal, PersonID: "person:main-e2e", AgentID: "agent", ConversationID: "main:agent", Title: goal, Done: "result checked"}
		if err = s.Create(h.ctx, task); err != nil {
			t.Fatal(err)
		}
		for _, suffix := range []string{"draft", "send", "independent"} {
			child := task
			child.ID = goal + ":" + suffix
			child.GoalID = goal
			if err = s.Create(h.ctx, child); err != nil {
				t.Fatal(err)
			}
		}
		questions := append([]waiting.Question(nil), script.Questions...)
		questions[0].Dependents = []string{goal + ":draft"}
		questions[1].Dependents = []string{goal + ":send"}
		r := waiting.Round{ID: goal, Goal: goal, Person: task.PersonID, Agent: task.AgentID, Conversation: task.ConversationID, Deadline: script.Deadline, Questions: questions}
		invalid := r
		invalid.Deadline = time.Time{}
		if err = rounds.Open(h.ctx, invalid); !errors.Is(err, waiting.ErrInvalid) {
			t.Fatalf("missing deadline accepted: %v", err)
		}
		if err = rounds.Open(h.ctx, r); err != nil {
			t.Fatal(err)
		}
		if err = start(s, goal+":draft"); !errors.Is(err, work.ErrState) {
			t.Fatalf("dependent escaped: %v", err)
		}
		if err = start(s, goal+":independent"); err != nil {
			t.Fatal(err)
		}
		if goal == script.StoppedGoal {
			if err = start(s, goal); err != nil {
				t.Fatal(err)
			}
			if err = s.Finish(h.ctx, goal, "stub:"+goal, "interrupted"); err != nil {
				t.Fatal(err)
			}
		}
	}
	closeWriter()
	// No explicit Expire/Recover call: boot must apply each default before ready.
	core = h.startCore()
	h.client(core)
	waitCoreReady(t, h)
	reader, err := canon.OpenReader(h.ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close(context.Background())
	read := work.New(reader)
	readRounds := waiting.NewWorkRounds(reader, read)
	for _, goal := range allGoals {
		r, err := readRounds.Get(h.ctx, goal)
		if err != nil || len(r.Decisions) != 1 || r.Decisions[0].Kind != "default" {
			t.Fatalf("Core did not expire %s: %+v %v", goal, r, err)
		}
		draft, _ := read.Get(h.ctx, goal+":draft")
		send, _ := read.Get(h.ctx, goal+":send")
		independent, _ := read.Get(h.ctx, goal+":independent")
		wantDraft := "working"
		if goal == script.StoppedGoal {
			wantDraft = "input_needed"
		}
		if draft.State != wantDraft || send.State != "input_needed" || !independent.Active {
			t.Fatalf("gating: %+v %+v %+v", draft, send, independent)
		}
	}
	client, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	receive := func(id string) protocol.Envelope {
		t.Helper()
		for {
			frame, err := client.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo != nil && *frame.ReplyTo == id {
				return frame
			}
		}
	}
	topic := "waiting"
	subscription, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	snapshotFrame := receive(subscription)
	snapshot, ok := snapshotFrame.Payload.(protocol.Snapshot)
	if !ok || len(snapshot.Items) != len(allGoals) {
		t.Fatalf("questions were not grouped by goal: %+v", snapshotFrame)
	}
	for _, item := range snapshot.Items {
		var question protocol.Question
		if err = json.Unmarshal(item.Payload, &question); err != nil {
			t.Fatal(err)
		}
		if question.Form == nil || len(question.Form.Fields) != len(script.Questions) {
			t.Fatalf("incomplete question round: %+v", question)
		}
	}
	// Refreshing from another subscription must not reset an in-progress form.
	subscription, err = client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
	if err != nil {
		t.Fatal(err)
	}
	againSnapshot := receive(subscription).Payload.(protocol.Snapshot)
	if !reflect.DeepEqual(snapshot, againSnapshot) {
		t.Errorf("unchanged work questions republished on subscription: before=%+v after=%+v", snapshot, againSnapshot)
	}
	assertResolved := func(id string) {
		t.Helper()
		request, err := client.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic})
		if err != nil {
			t.Fatal(err)
		}
		frame := receive(request)
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok {
			t.Fatalf("missing waiting snapshot: %+v", frame)
		}
		for _, item := range snapshot.Items {
			var question protocol.Question
			if err := json.Unmarshal(item.Payload, &question); err != nil {
				t.Fatal(err)
			}
			if question.Card != nil && question.Card.ID == id {
				if question.Card.Outcome == nil || *question.Card.Outcome != protocol.DecisionOutcomeResolved {
					t.Fatalf("late answer left question card open: %+v", question.Card)
				}
				return
			}
		}
		t.Fatalf("resolved question %s missing from snapshot", id)
	}
	goal := script.Goals[0]
	id, revision := "work:"+goal, "1"
	a, b := "B", waiting.DoNotAct
	values := protocol.FormValues{"route": {String: &a}, "send": {String: &b}}
	request, err := client.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values})
	if err != nil {
		t.Fatal(err)
	}
	frame := receive(request)
	if frame.Error != nil {
		t.Fatalf("late answer: %+v error=%+v %v", frame, frame.Error, err)
	}
	answer, ok := frame.Payload.(protocol.QuestionAnswer)
	if !ok || answer.Outcome == nil || *answer.Outcome != protocol.DecisionOutcomeResolved {
		t.Fatalf("late answer outcome: %+v", frame)
	}
	revised, err := read.Get(h.ctx, goal)
	if err != nil || revised.Revision != 2 || len(revised.Decisions) != 2 || revised.Decisions[1].Decision.MessageID != request || revised.Decisions[1].Decision.Message == "" {
		t.Fatalf("missing revision/message: %+v %v", revised, err)
	}
	assertResolved(id)
	// A different message from a stale surface cannot create another revision.
	request, err = client.AnswerQuestion(h.ctx, protocol.QuestionAnswer{CardID: &id, Revision: &revision, Answer: protocol.QuestionAnswerActionAccept, Values: &values})
	if err != nil {
		t.Fatal(err)
	}
	frame = receive(request)
	if frame.Error != nil {
		t.Fatalf("repeat answer: %+v", frame.Error)
	}
	afterRepeat, err := read.Get(h.ctx, goal)
	if err != nil || !reflect.DeepEqual(revised, afterRepeat) {
		t.Fatalf("resolved card accepted another decision: %+v %v", afterRepeat, err)
	}
	client.Close()
	h.stopCore(core)
	// A second boot must preserve both the default and the later decision.
	core = h.startCore()
	h.client(core)
	waitCoreReady(t, h)
	again, err := read.Get(h.ctx, goal)
	if err != nil || !reflect.DeepEqual(revised, again) {
		t.Fatalf("restart repeated effect: %+v %v", again, err)
	}
	client, err = clientapi.DialWithCapabilities(h.ctx, h.socket, "test", []protocol.Capability{{Name: "waiting", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	assertResolved(id)
	client.Close()
	h.stopCore(core)
	reader.Close(context.Background())
	db, closeWriter = openWriter()
	s = work.New(db)
	stopped, err := s.Get(h.ctx, script.StoppedGoal)
	if err != nil || stopped.State != "stopped" {
		t.Fatalf("stopped goal resurrected: %+v %v", stopped, err)
	}
	if err = start(s, script.StoppedGoal+":draft"); !errors.Is(err, work.ErrState) {
		t.Fatalf("stopped goal released dependent: %v", err)
	}
	workerCalls, pluginCalls := 0, 0
	for _, goal := range script.Goals {
		if err = start(s, goal+":draft"); err != nil {
			t.Fatal(err)
		}
		workerCalls++
		// The plugin is behind the real work admission gate, for both goal kinds.
		if err = start(s, goal+":send"); err == nil {
			pluginCalls++
		} else if !errors.Is(err, work.ErrState) {
			t.Fatal(err)
		}
	}
	if workerCalls != len(script.Goals) || pluginCalls != 0 {
		t.Fatalf("stub calls: workers=%d plugin=%d", workerCalls, pluginCalls)
	}
	closeWriter()
}
