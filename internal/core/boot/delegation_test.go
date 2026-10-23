// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/clientsrv"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/guard"
	"github.com/thisisjun786/lina/internal/core/ledger"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/secrets"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/internal/core/worker/codex"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestDelegationRejectsUnretainedChannel(t *testing.T) {
	r, _, agent, other := managementFixture(t)
	r.agent, r.conversation = agent, "main:"+agent
	channel := &conversationRuntime{db: r.db, person: r.person, agent: agent, conversation: "channel-fixture"}
	r.router = &conversationRouter{root: r, views: map[string]*conversationRuntime{agent: r, channel.conversation: channel}}
	now := time.Now()
	input, err := r.journal().Admit(t.Context(), guard.EntryOwner, ledger.Input{ID: "channel-task-input", PersonID: r.person, AgentID: agent, ConversationID: channel.conversation, Surface: ledger.Surface{Kind: "channel", Platform: "discord", ChatID: "fixture"}, OccurredAt: now, RecordedAt: now, TimeQuality: "observed", Body: "Run the scoped task."})
	if err != nil {
		t.Fatal(err)
	}
	d := &delegationRuntime{root: r}
	task := work.Task{PersonID: r.person, AgentID: agent, ConversationID: channel.conversation, Schedule: &work.Schedule{InputID: input.ID}}
	if _, _, err := d.origin(t.Context(), task); err == nil {
		t.Fatal("unretained channel input acquired the cached conversation's permissions")
	}
	for _, foreign := range []work.Task{
		{PersonID: "another", AgentID: agent, ConversationID: channel.conversation, Schedule: task.Schedule},
		{PersonID: r.person, AgentID: agent, ConversationID: r.conversation, Schedule: task.Schedule},
	} {
		if _, _, err := d.origin(t.Context(), foreign); err == nil {
			t.Fatal("foreign origin accepted")
		}
	}
	channel.agent = other
	if _, _, err := d.origin(t.Context(), task); err == nil {
		t.Fatal("rebound conversation accepted old agent")
	}
}

func TestDelegationDistinguishesTerminalConnectionFailure(t *testing.T) {
	for _, code := range []string{"connection", "connection_lost"} {
		t.Run(code, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			ctl, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { ctl.Close() })
			if err = ctl.Acquire(); err != nil {
				t.Fatal(err)
			}
			db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: 1, Controls: ctl, Migrations: canon.Migrations()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close(context.Background()) })
			service := work.New(db)
			changed := make(chan struct{}, 4)
			reaction := reactor.NewScheduler(service, reactor.Hooks{Availability: func(context.Context) (work.Availability, error) {
				return work.Availability{Hosts: map[string]int{"local": 1}}, nil
			}, Start: func(context.Context, work.Card) (work.Run, error) {
				return work.Run{ID: "run", TaskID: "task", Generation: 1, Worker: "codex", NodeID: "local", ThreadID: "thread", TurnID: "turn"}, nil
			}, Changed: func(context.Context) { changed <- struct{}{} }})
			task := work.Task{ID: "task", AgentID: "agent", ConversationID: "conversation", Title: "task", Done: "checked", Schedule: &work.Schedule{Group: "group", Repository: "owner/repo", Areas: []string{"src/**"}, Worker: "codex", Host: "local"}}
			if err = reaction.Schedule(t.Context(), []work.Task{task}); err != nil {
				t.Fatal(err)
			}
			<-changed
			activity := lease.New(func() {})
			ctx, cancel := context.WithCancel(t.Context())
			d := &delegationRuntime{root: &conversationRuntime{work: service}, reactor: reaction, adapter: &codex.Session{}, done: make(chan struct{}), workerEvents: make(chan worker.Event, 1), leases: map[string]lease.Lease{"thread:turn": activity}}
			origin := &conversationRuntime{ctx: ctx, decisions: &clientsrv.Decisions{}}
			origin.router = &conversationRouter{root: origin}
			entry := &workerSession{origin: origin, questions: map[string]*workerQuestion{"question": {card: protocol.Question{Card: &protocol.DecisionCard{}}, event: worker.Event{Thread: worker.Thread{TurnID: "turn"}}}}}
			d.root.workers = &workerRuntime{sessions: map[string]*workerSession{"thread": entry}}
			go d.events(ctx)
			t.Cleanup(func() { cancel(); <-d.done; activity.Release() })
			d.workerEvents <- worker.Event{Thread: worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task"}, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: code}}
			select {
			case <-changed:
			case <-t.Context().Done():
				t.Fatal("completion not observed")
			}
			cancel()
			<-d.done
			if stopped := entry.questions["question"].card.Card.Outcome != nil; stopped != (code != "connection_lost") {
				t.Fatalf("terminal request withdrawal=%v for %s", stopped, code)
			}
			card, err := service.Get(t.Context(), "task")
			if err != nil {
				t.Fatal(err)
			}
			lost := code == "connection_lost"
			if card.Active != lost || card.Scheduling.CapacityHeld != lost || !card.Scheduling.AreaHeld {
				t.Fatalf("%s: active=%v capacity=%v area=%v", code, card.Active, card.Scheduling.CapacityHeld, card.Scheduling.AreaHeld)
			}
		})
	}
}

func TestDelegatedIdentityIsStableAndSafeMetadata(t *testing.T) {
	id := delegatedID("turn", "call", "key")
	masked, err := secrets.Mask(id)
	if err != nil || masked.Text() != id {
		t.Fatalf("generated task identity is secret-shaped: %q %v", id, err)
	}
	if delegatedID("turn", "call", "key") != id || delegatedID("turn", "call", "other") == id {
		t.Fatal("unstable or colliding task identity")
	}
}
