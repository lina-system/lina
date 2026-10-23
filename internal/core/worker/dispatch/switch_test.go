// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

type switchLeaser struct{}

func (switchLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

func switchDispatchFixture(t *testing.T, held bool) (*Service, Record, string, *os.File, *os.File) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-C", repo}, args...)...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	runGit("init", "-b", "dev")
	if err := os.WriteFile(filepath.Join(repo, "design.md"), []byte("# Result\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "design.md")
	runGit("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "Initial design")
	runGit("remote", "add", "origin", "https://github.com/fixture/project.git")
	marker := filepath.Join(root, "started")
	env = append(env, "STARTED="+marker)
	var ready, release *os.File
	if held {
		for _, name := range []string{"ready", "release"} {
			path := filepath.Join(root, name)
			if out, err := exec.Command("mkfifo", path).CombinedOutput(); err != nil {
				t.Fatalf("fifo: %v %s", err, out)
			}
			file, err := os.OpenFile(path, os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { file.Close() })
			if name == "ready" {
				ready = file
				env = append(env, "READY="+path)
			} else {
				release = file
				env = append(env, "RELEASE="+path)
			}
		}
	}
	binary := filepath.Join(root, "agy")
	script := `#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
if [ "$1" = --version ]; then echo fixture; exit; fi
cat >/dev/null
printf started > "$STARTED"
printf '{"event":"init","conversation_id":"fixture","init":{"cwd":"%s"}}\n' "$PWD"
if [ -n "$READY" ]; then
  printf 'ready\n' > "$READY"
  read mode < "$RELEASE"
  if [ "$mode" = unknown ]; then exit; fi
fi
printf '{"event":"result","result":{"conversation_id":"fixture","status":"ERROR","error":"fixture failure"}}\n'
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	// Each supervised command also runs the race runtime's exit cleanup. This
	// scenario launches several Git commands before the worker's own deadlines.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	s := &Service{ctx: ctx, cfg: Config{DB: store(t), Root: filepath.Join(root, "state"), Main: "main", Epoch: 1, Git: git, Binary: binary, Environment: env, Activities: switchLeaser{}}}
	if err := os.MkdirAll(s.cfg.Root, 0700); err != nil {
		t.Fatal(err)
	}
	s.cfg.Workers = registerBinaries(s.cfg)
	s.cfg.Work = work.New(s.cfg.DB)
	s.cfg.Area, err = work.OpenArea(s.cfg.Root, git)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.cfg.Area.Close(context.Background()) })
	d := definition(t)
	d.Repository = repo
	d.Worker = "antigravity"
	result := s.Tool(Origin{Person: "owner", Agent: "agent", Conversation: "conversation", Turn: "source-turn"}).Execute(ctx, provider.ToolCall{ID: "call", Name: "dispatch_work", Arguments: string(mustJSON(d))})
	if result.Code != loop.OK {
		t.Fatal(result)
	}
	records, err := s.records(ctx)
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	r := records[0]
	r.State = "running"
	if err := s.save(ctx, r); err != nil {
		t.Fatal(err)
	}
	return s, r, marker, ready, release
}

func externalDispatchTask(t *testing.T, s *Service, roots []string, activate bool) *work.Service {
	t.Helper()
	w := work.New(s.cfg.DB)
	if err := w.Create(t.Context(), work.Task{ID: "external", PersonID: "owner", AgentID: "agent", ConversationID: "conversation", Title: "External task", Done: "Result", ExternalControl: true, ArtifactRoots: roots}); err != nil {
		t.Fatal(err)
	}
	if activate {
		if err := w.ApproveSwitch(t.Context(), work.Switch{ID: "switch", Owner: "owner", Approval: "approval", Snapshot: strings.Repeat("a", 64), Tasks: []string{"external"}}); err != nil {
			t.Fatal(err)
		}
		if err := w.ActivateSwitch(t.Context(), "switch", "owner", "stopped"); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

func TestDispatchSwitchRefusesWithoutControl(t *testing.T) {
	for _, area := range []string{"repository", "worktree", "artifacts", "clone", "git_metadata", "rollback"} {
		t.Run(area, func(t *testing.T) {
			s, r, marker, _, _ := switchDispatchFixture(t, false)
			root := r.Definition.Repository
			if area == "worktree" {
				root = filepath.Join(s.cfg.Root, "work", "tasks", r.ID)
			} else if area == "artifacts" {
				root = filepath.Join(s.cfg.Root, "work", "runs", r.ID)
			} else if area == "clone" {
				root = filepath.Join(s.cfg.Root, "work", "clones", fmt.Sprintf("%x", sha256.Sum256([]byte(root))))
			} else if area == "git_metadata" {
				// A linked worktree writes the original repository's Git metadata
				// even though the two working directories are disjoint.
				other := filepath.Join(filepath.Dir(root), "linked")
				cmd := exec.Command(s.cfg.Git, "-C", root, "worktree", "add", "--detach", other, "dev")
				cmd.Env = s.cfg.Environment
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("linked worktree: %v %s", err, out)
				}
				r.Definition.Repository = other
				if err := s.save(t.Context(), r); err != nil {
					t.Fatal(err)
				}
			}
			w := externalDispatchTask(t, s, []string{root}, area == "rollback")
			if area == "rollback" {
				if err := w.RequestRollback(t.Context(), "switch", "owner", "rollback"); err != nil {
					t.Fatal(err)
				}
			}
			s.execute(s.ctx, r)
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("dispatch launched without switch activation", err)
			}
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 || records[0].Code != "control_held" {
				t.Fatal("dispatch did not report the switch gate refusal", records, err)
			}
			if err := s.cfg.DB.Read(t.Context(), func(tx *canon.Tx) error {
				var count int
				if err := tx.QueryRowContext(t.Context(), "SELECT count(*) FROM work_worker_reservations").Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					t.Fatal("refused directory batch left partial reservations", count)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDispatchSwitchTracksExecutionThroughRollback(t *testing.T) {
	for _, outcome := range []string{"finished", "unknown", "shutdown"} {
		t.Run(outcome, func(t *testing.T) {
			s, r, _, ready, release := switchDispatchFixture(t, true)
			// All reserved paths overlap one task: duplicate directory matches must
			// retain one task binding without rejecting the admitted execution.
			w := externalDispatchTask(t, s, []string{filepath.Dir(r.Definition.Repository)}, true)
			ctx, cancel := context.WithCancel(s.ctx)
			s.ctx = ctx
			done := make(chan struct{})
			go func() { defer close(done); s.execute(s.ctx, r) }()
			t.Cleanup(func() { cancel(); <-done })
			observed := make(chan error, 1)
			go func() { _, err := bufio.NewReader(ready).ReadString('\n'); observed <- err }()
			select {
			case err := <-observed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("worker did not acknowledge start", ctx.Err())
			case <-done:
				records, err := s.records(t.Context())
				t.Fatal("dispatch ended before worker acknowledgement", records, err)
			}
			if err := w.RequestRollback(t.Context(), "switch", "owner", "rollback"); err != nil {
				t.Fatal(err)
			}
			if err := w.CompleteRollback(t.Context(), "switch", "owner", "drained"); !errors.Is(err, work.ErrActiveRun) {
				t.Fatal("rollback ignored the delegated worker", err)
			}
			if outcome == "shutdown" {
				cancel()
			} else if _, err := release.WriteString(outcome + "\n"); err != nil {
				t.Fatal(err)
			}
			<-done
			if outcome != "finished" {
				if err := w.Recover(t.Context(), "restart"); err != nil {
					t.Fatal(err)
				}
				restarted, err := Open(t.Context(), s.cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := restarted.Close(t.Context()); err != nil {
					t.Fatal(err)
				}
				records, err := restarted.records(t.Context())
				if err != nil || len(records) != 1 || records[0].State != "unknown" {
					t.Fatal("recovery lost the uncertain dispatch", records, err)
				}
				if err := w.CompleteRollback(t.Context(), "switch", "owner", "drained"); !errors.Is(err, work.ErrActiveRun) {
					t.Fatal("uncertain dispatch lost its durable reservation", err)
				}
			} else if err := w.CompleteRollback(t.Context(), "switch", "owner", "drained"); err != nil {
				t.Fatal("terminal dispatch retained its reservation", err)
			}
		})
	}
}

type uncertainSwitchAdapter struct {
	quotaStopAdapter
	started *int
}

func (a *uncertainSwitchAdapter) Name() string { return "second" }
func (a *uncertainSwitchAdapter) Start(context.Context, lease.Lease, worker.StartSpec) (worker.Thread, error) {
	(*a.started)++
	return worker.Thread{}, errors.New("launch outcome unknown")
}

func TestDispatchSwitchFencesQuotaReplacement(t *testing.T) {
	for _, rollback := range []bool{true, false} {
		t.Run(fmt.Sprintf("rollback=%t", rollback), func(t *testing.T) {
			s, r, _, _, _ := switchDispatchFixture(t, false)
			r.Definition.Worker = ""
			if err := s.save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			w := externalDispatchTask(t, s, []string{r.Definition.Repository}, true)
			starts := 0
			rolledBack := false
			s.cfg.Workers = []Factory{
				{Name: "first", Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
					return &quotaStopAdapter{events: make(chan worker.Event, 1)}, func(context.Context) error {
						if rollback && !rolledBack {
							rolledBack = true
							return w.RequestRollback(t.Context(), "switch", "owner", "quota handoff")
						}
						return nil
					}, nil
				}},
				{Name: "second", Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
					return &uncertainSwitchAdapter{started: &starts}, func(context.Context) error { return nil }, nil
				}},
			}
			s.execute(s.ctx, r)
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			if rollback {
				if starts != 0 || records[0].Code != "control_held" {
					t.Fatalf("rollback launched replacement: starts=%d state=%s code=%s", starts, records[0].State, records[0].Code)
				}
				if err := w.CompleteRollback(t.Context(), "switch", "owner", "first worker settled"); err != nil {
					t.Fatal(err)
				}
			} else {
				if starts != 1 || records[0].State != "unknown" {
					t.Fatalf("uncertain replacement was settled: starts=%d state=%s code=%s", starts, records[0].State, records[0].Code)
				}
				if err := w.RequestRollback(t.Context(), "switch", "owner", "unknown replacement"); err != nil {
					t.Fatal(err)
				}
				if err := w.CompleteRollback(t.Context(), "switch", "owner", "unproven completion"); !errors.Is(err, work.ErrActiveRun) {
					t.Fatalf("uncertain replacement lost reservation: %v", err)
				}
			}
		})
	}
}

type connectionSwitchAdapter struct {
	quotaStopAdapter
	name, code string
}

func (a *connectionSwitchAdapter) Name() string { return a.name }
func (a *connectionSwitchAdapter) Start(ctx context.Context, l lease.Lease, spec worker.StartSpec) (worker.Thread, error) {
	thread, err := a.quotaStopAdapter.Start(ctx, l, spec)
	event := <-a.events
	event.Finish = &worker.Finish{Status: worker.Failed, Failure: worker.ConnectionFailure, Code: a.code}
	a.events <- event
	return thread, err
}

// The recovery capability alone cannot settle unknown effects. Its observed
// terminal snapshot must survive the attempt/delivery function boundary.
type recoverableConnectionAdapter struct{ *connectionSwitchAdapter }

func (a *recoverableConnectionAdapter) RecoverAttempt(context.Context, lease.Lease, worker.StartSpec, worker.Thread) (worker.Thread, error) {
	return worker.Thread{}, errors.New("unexpected recovery during connection evidence check")
}

func TestDispatchSwitchConnectionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, code           string
		terminal             bool
		recoverable, unknown bool
	}{
		{"claude", "invalid_message", false, false, false},
		{"claude", "session_mismatch", false, false, false},
		{"claude", "invalid_result", false, false, false},
		{"claude", "invalid_init", false, false, false},
		{"claude", "invalid_usage", false, false, false},
		{"claude", "missing_result", false, false, false},
		{"antigravity", "missing_result", false, false, false},
		{"codex", "connection_lost", false, false, false},
		{"codex", "connection", true, false, false},
		{"gemini", "joined_failure", true, true, false},
		{"gemini", "unknown_failure", false, true, true},
	} {
		t.Run(tc.name+"/"+tc.code, func(t *testing.T) {
			s, r, _, _, _ := switchDispatchFixture(t, false)
			r.Definition.Worker = tc.name
			if err := s.save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			w := externalDispatchTask(t, s, []string{r.Definition.Repository}, true)
			s.cfg.Workers = []Factory{{Name: tc.name, Explicit: true, Chooser: true, Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
				a := &connectionSwitchAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 1), unknown: tc.unknown}, name: tc.name, code: tc.code}
				if tc.recoverable {
					return &recoverableConnectionAdapter{a}, func(context.Context) error { return nil }, nil
				}
				return a, func(context.Context) error { return nil }, nil
			}}}
			s.execute(s.ctx, r)
			records, err := s.records(t.Context())
			if err != nil || len(records) != 1 {
				t.Fatal(records, err)
			}
			wantState := "unknown"
			if tc.terminal {
				wantState = "finished"
			}
			if records[0].State != wantState || records[0].Code != tc.code {
				t.Errorf("connection evidence misclassified: state=%s code=%s", records[0].State, records[0].Code)
			}
			if err := w.Recover(t.Context(), "restart"); err != nil {
				t.Fatal(err)
			}
			card, err := w.Get(t.Context(), r.ID)
			if err != nil || card.Active == tc.terminal || (card.Receipt != nil) != tc.terminal {
				t.Errorf("connection outcome lost: card=%+v err=%v", card, err)
			}
			if err := w.RequestRollback(t.Context(), "switch", "owner", "connection failure"); err != nil {
				t.Fatal(err)
			}
			err = w.CompleteRollback(t.Context(), "switch", "owner", "check observed outcome")
			if tc.terminal && err != nil || !tc.terminal && !errors.Is(err, work.ErrActiveRun) {
				t.Fatal("rollback ignored connection evidence", err)
			}
		})
	}
}
