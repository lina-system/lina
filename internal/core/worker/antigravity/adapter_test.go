// SPDX-License-Identifier: Apache-2.0
package antigravity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/proc"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "proc-supervise" {
		os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))
	}
	os.Exit(m.Run())
}

type leaser struct{}

func (leaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

func fixture(t *testing.T, body string) (*Adapter, context.Context, lease.Lease, string) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "agy")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = --version ]; then echo fixture-1; exit; fi\ncat >/dev/null\n"+body), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	a := New(Config{Binary: binary, Environment: []string{"PATH=/usr/bin:/bin"}, Epoch: 7, Leaser: leaser{}})
	l := lease.New(func() {})
	t.Cleanup(func() {
		cancel()
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
		l.Release()
	})
	return a, ctx, l, dir
}
func recorded(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return "cat <<EOF\n" + strings.ReplaceAll(string(raw), "WORKDIR", "$PWD") + "EOF\n"
}
func finish(t *testing.T, ctx context.Context, a *Adapter) worker.Finish {
	t.Helper()
	for {
		select {
		case e := <-a.Events():
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			if e.Finish != nil {
				return *e.Finish
			}
		case <-ctx.Done():
			t.Fatal("no terminal result", ctx.Err())
			return worker.Finish{}
		}
	}
}
func TestResultEndsTurnBeforeProcessExit(t *testing.T) {
	a, ctx, _, dir := fixture(t, recorded(t, "success")+"mkfifo hold\nread line < hold\n")
	released := make(chan struct{})
	l := lease.New(func() { close(released) })
	thread, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "perform task"})
	if err != nil {
		t.Fatal(err)
	}
	if got := finish(t, ctx, a); got.Status != worker.Done {
		t.Fatal(got)
	}
	snapshot, err := a.Snapshot(ctx, thread)
	if err != nil || snapshot.State != worker.Idle {
		t.Fatalf("result did not end live turn: %+v %v", snapshot, err)
	}
	l.Release()
	select {
	case <-released:
		t.Fatal("result released a live process lease")
	default:
	}
	if err = a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-released:
	case <-ctx.Done():
		t.Fatal("reaped process retained lease")
	}
}
func TestSoftDenialIsFailureEvenWithSuccessEnvelope(t *testing.T) {
	a, ctx, l, dir := fixture(t, recorded(t, "denied"))
	_, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "perform task"})
	if err != nil {
		t.Fatal(err)
	}
	got := finish(t, ctx, a)
	if got.Status != worker.Failed || got.Code != "denied_actions" {
		t.Fatal(got)
	}
	if len(a.Result().DeniedActions) != 1 {
		t.Fatal("denied tool lost")
	}
}
func TestExitZeroWithoutResultFails(t *testing.T) {
	init := `printf '{"event":"init","conversation_id":"fixture","init":{"cwd":"%s"}}\n' "$PWD"` + "\n"
	for _, suffix := range []string{"exit 0\n", "echo invalid\n", `echo '{"event":"result","result":{"conversation_id":"other","status":"SUCCESS"}}'` + "\n"} {
		t.Run(fmt.Sprint(len(suffix)), func(t *testing.T) {
			a, ctx, l, dir := fixture(t, init+suffix)
			_, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"})
			if err != nil {
				t.Fatal(err)
			}
			if got := finish(t, ctx, a); got.Status != worker.Failed {
				t.Fatal(got)
			}
		})
	}
}
func TestStaleControlAndCapabilities(t *testing.T) {
	a, ctx, l, dir := fixture(t, recorded(t, "success"))
	if _, err := a.Start(ctx, l, worker.StartSpec{Required: worker.CapabilitySet{worker.RequestApprovals}}); err == nil {
		t.Fatal("advertised unsupported approval control")
	}
	thread, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"})
	if err != nil {
		t.Fatal(err)
	}
	thread.BindingEpoch++
	if err := a.Interrupt(ctx, thread, worker.UserCancel); err == nil {
		t.Fatal("stale epoch controlled child")
	}
}
