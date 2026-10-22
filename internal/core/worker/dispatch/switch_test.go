// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/work"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/proc"
	"github.com/thisisjun786/lina/kit/provider"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

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
	d := definition(t)
	d.Repository = repo
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
	for _, area := range []string{"repository", "worktree", "artifacts", "git_metadata", "rollback"} {
		t.Run(area, func(t *testing.T) {
			s, r, marker, _, _ := switchDispatchFixture(t, false)
			root := r.Definition.Repository
			if area == "worktree" {
				root = filepath.Join(s.cfg.Root, "work", "worktrees", r.ID)
			} else if area == "artifacts" {
				root = filepath.Join(s.cfg.Root, "work", "tasks", r.ID)
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
			s.execute(r)
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
			// All three paths overlap one task: duplicate directory matches must
			// retain one task binding without rejecting the admitted execution.
			w := externalDispatchTask(t, s, []string{filepath.Dir(r.Definition.Repository)}, true)
			ctx, cancel := context.WithCancel(s.ctx)
			s.ctx = ctx
			done := make(chan struct{})
			go func() { defer close(done); s.execute(r) }()
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
