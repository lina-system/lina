// SPDX-License-Identifier: Apache-2.0
package runs_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/assets"
	"github.com/thisisjun786/lina/internal/core/budget"
	"github.com/thisisjun786/lina/internal/core/canon"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/internal/core/inbox"
	"github.com/thisisjun786/lina/internal/core/runs"
	"github.com/thisisjun786/lina/internal/core/turn"
	"github.com/thisisjun786/lina/kit/loop"
	"github.com/thisisjun786/lina/kit/provider"
)

func promotionCrashService(t *testing.T, root string, epoch uint64) (*runs.Service, *canon.Store) {
	t.Helper()
	controls, err := ctlfiles.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controls.Acquire(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { controls.Close() })
	ms := canon.Migrations()
	db, err := canon.Open(t.Context(), canon.Config{Path: filepath.Join(root, "canon.sqlite"), Epoch: epoch, Controls: controls, Migrations: ms})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(context.Background()) })
	materials, err := assets.Open(t.Context(), root, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { materials.Close() })
	s, err := runs.New(runs.Config{DB: db, Assets: materials, WorkDir: filepath.Join(root, "work"), Epoch: epoch,
		PublishPromotion: turn.PublishPromotion,
		Authorize:        func(context.Context, runs.Origin, runs.Brief) error { return nil },
		Stream: func(context.Context, provider.Request) (provider.Stream, error) {
			file, err := os.OpenFile(filepath.Join(root, "model-calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			_, err = file.WriteString("called\n")
			closeErr := file.Close()
			return answer(runs.Artifact{Body: "remaining work complete", Done: true}), errors.Join(err, closeErr)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, db
}

func TestKillAfterPromotionAdmissionPublishesHandleAndResumesOnce(t *testing.T) {
	t.Parallel()
	if root := os.Getenv("LINA_PROMOTION_CRASH_ROOT"); root != "" {
		service, db := promotionCrashService(t, root, 1)
		o := origin()
		id, err := inbox.New(db).Admit(t.Context(), inbox.Input{Key: inbox.Key{Conversation: o.Conversation, Source: "client", NativeID: "input"}, Body: []byte("whole request")})
		if err != nil {
			t.Fatal(err)
		}
		gauge := (budget.Meter{}).Start(o.Turn, time.Now())
		for range 10 {
			gauge.Round()
		}
		_, err = turn.New(db).Run(t.Context(), turn.Options{ID: o.Turn, Conversation: o.Conversation, InboxID: id.ID, Budget: gauge,
			Loop: loop.Config{Stream: func(context.Context, provider.Request) (provider.Stream, error) {
				return nil, errors.New("turn replayed")
			}},
			StartFromTurn: func(ctx context.Context, h budget.Handoff) (budget.Handle, error) {
				handle, err := service.StartFromTurn(ctx, o, brief(), h)
				if err != nil {
					return handle, err
				}
				os.Stdout.WriteString("admitted\n")
				var input [1]byte
				os.Stdin.Read(input[:]) // parent kills before Store.Run receives the handle
				return handle, nil
			},
		})
		t.Fatalf("child resumed: %v", err)
	}
	root := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "-test.run=^TestKillAfterPromotionAdmissionPublishesHandleAndResumesOnce$")
	child.Env = []string{"LINA_PROMOTION_CRASH_ROOT=" + root}
	output, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			child.Process.Kill()
			child.Wait()
		}
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "admitted\n" {
		t.Fatalf("admission barrier: %q %v", line, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil || ctx.Err() != nil {
		t.Fatalf("child did not stop at admission: %v", err)
	}
	service, db := promotionCrashService(t, root, 2)
	store := turn.New(db)
	if err := store.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	outcome, err := store.Outcome(t.Context(), origin().Turn)
	if err != nil || outcome.Handle.ID == "" || outcome.Meter.Level != budget.Hard {
		t.Fatalf("admitted run lost its durable handle/meter: %+v %v", outcome, err)
	}
	if err := service.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Wait(ctx, outcome.Handle.ID); err != nil {
		t.Fatal(err)
	}
	row, err := service.Get(t.Context(), outcome.Handle.ID)
	if err != nil || row.Outcome != runs.Complete || row.Retries != 0 {
		t.Fatalf("recovered promotion: %+v %v", row, err)
	}
	called, err := os.ReadFile(filepath.Join(root, "model-calls"))
	if err != nil || string(called) != "called\n" {
		t.Fatalf("promotion was lost or executed twice: %q %v", called, err)
	}
}
