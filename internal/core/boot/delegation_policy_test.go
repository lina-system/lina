// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/agents"
	"github.com/thisisjun786/lina/internal/core/authority"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/reactor"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

const policyGraph = `{"repository":"fixture/repo","tasks":[{"key":"one","what":"Write result","why":"Exercise scheduling","done":"result committed","areas":["result/**"],"depends_on":[],"design":["DESIGN.md#work"]}]}`

func delegationPolicyRuntime(t *testing.T) (*conversationRuntime, *delegationRuntime, *int) {
	t.Helper()
	r := controlRuntime(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	if err := os.WriteFile(filepath.Join(clone, "DESIGN.md"), []byte("# Design\n\n## Work\n\nCommit the result.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-b", "dev"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "fixture"}} {
		command := exec.Command(git, append([]string{"-C", clone}, args...)...)
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %v %s", err, output)
		}
	}
	d := &delegationRuntime{root: r, processes: proc.NewOwner(), config: delegationConfig{Git: git, Repositories: map[string]delegationRepository{"fixture/repo": {Clone: clone, Base: "dev"}}}}
	t.Cleanup(func() { _ = d.processes.Close(context.Background()) })
	started := 0
	d.reactor = reactor.NewScheduler(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 4}}, nil
		},
		Start: func(_ context.Context, c work.Card) (work.Run, error) {
			started++
			return work.Run{ID: c.Task.ID + ":1", TaskID: c.Task.ID, Generation: 1, Worker: "codex", NodeID: "local", ThreadID: "thread-" + c.Task.ID, TurnID: "turn"}, nil
		},
	})
	return r, d, &started
}

func TestDelegationPresetCeilingsPrecedeAdmissionAndSurviveRelay(t *testing.T) {
	for _, preset := range []string{"investing", "legal"} {
		t.Run(preset, func(t *testing.T) {
			r, d, started := delegationPolicyRuntime(t)
			source, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Dev administrator", Preset: preset})
			if err != nil {
				t.Fatal(err)
			}
			receiver, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Receiver", Preset: "dev"})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.personaAuthority.BindDelegation(t.Context(), r.person, source.ID, "source-turn", receiver.ID, "turn:relay-input"); err != nil {
				t.Fatal(err)
			}
			for _, attempt := range []struct{ agent, turn string }{{source.ID, "turn:direct-input"}, {receiver.ID, "turn:relay-input"}} {
				view := newConversationView(r, attempt.agent, "main:"+attempt.agent)
				view.delegation = d
				bindings := map[string]loop.Tool{}
				view.prepareDelegationTools(attempt.turn, bindings)
				call := provider.ToolCall{ID: "graph", Name: "schedule_work", Arguments: policyGraph}
				for range 2 {
					result := bindings[call.Name].Execute(t.Context(), call)
					if result.Code != loop.Denied {
						t.Errorf("%s: preset ceiling bypassed: %+v", attempt.turn, result)
					}
				}
			}
			cards, err := r.work.Cards(t.Context())
			if err != nil || len(cards) != 0 || *started != 0 {
				t.Fatalf("refused graph reached admission/dispatch: cards=%d started=%d err=%v", len(cards), *started, err)
			}
		})
	}
}

func TestDelegationRecoveryChecksInheritedPresetBeforeSetup(t *testing.T) {
	r, d, _ := delegationPolicyRuntime(t)
	source, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Origin", Preset: "legal"})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := r.agents.Create(t.Context(), agents.Create{PersonID: r.person, Name: "Receiver", Preset: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.personaAuthority.BindDelegation(t.Context(), r.person, source.ID, "origin-turn", receiver.ID, "turn:queued-input"); err != nil {
		t.Fatal(err)
	}
	task := work.Task{ID: "queued-task", PersonID: r.person, AgentID: receiver.ID, ConversationID: "main:" + receiver.ID, Schedule: &work.Schedule{InputID: "queued-input", Repository: "fixture/repo", Worker: "codex", Host: "local"}}
	if _, err = d.start(t.Context(), work.Card{Task: task}); !errors.Is(err, authority.ErrPresetLimit) || !errors.Is(err, reactor.ErrNotStarted) {
		t.Fatalf("recovery bypassed origin ceiling before setup: %v", err)
	}
	if _, err = d.returnConflict(t.Context(), work.Card{Task: task}); !errors.Is(err, authority.ErrPresetLimit) {
		t.Fatalf("conflict return bypassed origin ceiling: %v", err)
	}
}

func TestDelegationBindsOriginPolicyBeforeDispatch(t *testing.T) {
	r, d, _ := delegationPolicyRuntime(t)
	prepareScheduledManagerRuntime(t, r, &scheduledManagerAdapter{r: r})
	view := newConversationView(r, r.agent, r.conversation)
	view.delegation = d
	var started bool
	d.reactor = reactor.NewScheduler(r.work, reactor.Hooks{
		Availability: func(context.Context) (work.Availability, error) {
			return work.Availability{Hosts: map[string]int{"local": 1}}, nil
		},
		Start: func(ctx context.Context, c work.Card) (work.Run, error) {
			started = true
			var source, parent, receiver string
			if err := r.db.Read(ctx, func(tx *canon.Tx) error {
				return tx.QueryRowContext(ctx, "SELECT source,source_task,receiver FROM authority_delegations WHERE task_id=?", c.Task.ID).Scan(&source, &parent, &receiver)
			}); err != nil || source != r.agent || parent != "turn:allowed-input" || receiver != c.Task.AgentID {
				t.Fatalf("dispatch lost origin binding: %q %q %q %v", source, parent, receiver, err)
			}
			return work.Run{}, reactor.ErrNotStarted // No actual worker needed for this boundary.
		},
	})
	bindings := map[string]loop.Tool{}
	view.prepareDelegationTools("turn:allowed-input", bindings)
	bindings["schedule_work"].Execute(t.Context(), provider.ToolCall{ID: "graph", Name: "schedule_work", Arguments: policyGraph})
	if !started {
		t.Fatal("unrestricted origin could not reach dispatch")
	}
}
