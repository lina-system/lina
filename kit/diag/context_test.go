// SPDX-License-Identifier: Apache-2.0
package diag_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/thisisjun786/lina/kit/diag"
)

type contextWriter func(context.Context, diag.Event) error

func (w contextWriter) Append(ctx context.Context, e diag.Event) error { return w(ctx, e) }

func TestTerminalEmitterHasIndependentBoundedContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		running, stop := context.WithCancel(context.Background())
		stop()
		start := time.Now()
		called := false
		emitter := diag.NewEmitter(contextWriter(func(ctx context.Context, _ diag.Event) error {
			called = true
			if err := ctx.Err(); err != nil {
				t.Fatalf("inherited cancellation: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok || deadline.Sub(start) != 5*time.Second {
				t.Fatal("missing five-second commit budget")
			}
			<-ctx.Done()
			return ctx.Err()
		}))
		_, err := emitter.Emit(running, diag.Fields{Component: diag.Task, Cause: diag.Cause{Code: diag.TaskStopped, ParentEventID: id(t)}, Result: diag.Cancelled, NextAction: diag.ReportFailure, ErrorClass: diag.Interrupted})
		if !called || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 5*time.Second {
			t.Fatalf("unbounded or skipped terminal commit: called=%v err=%v", called, err)
		}
	})
}

func TestNestedCommitDoesNotRenewBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		outer, cancel := diag.CommitContext(context.Background(), diag.Cancelled)
		defer cancel()
		time.Sleep(4 * time.Second)
		inner, stop := diag.CommitContext(outer, diag.Cancelled)
		defer stop()
		<-inner.Done()
		if !errors.Is(inner.Err(), context.DeadlineExceeded) || time.Since(start) != 5*time.Second {
			t.Fatal("writer renewed terminal commit budget")
		}
	})
}
