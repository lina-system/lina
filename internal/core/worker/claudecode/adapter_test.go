// SPDX-License-Identifier: Apache-2.0
package claudecode

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
	binary := filepath.Join(dir, "claude")
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
	if got.Status != worker.Failed || got.Code != "permission_denials" {
		t.Fatal(got)
	}
	if len(a.Result().PermissionDenials) != 1 {
		t.Fatal("denied tool lost")
	}
}
func TestExitZeroWithoutResultFails(t *testing.T) {
	init := `printf '{"type":"system","subtype":"init","session_id":"fixture","cwd":"%s"}\n' "$PWD"` + "\n"
	for _, suffix := range []string{"exit 0\n", "echo invalid\n", `echo '{"type":"result","subtype":"success","session_id":"other","is_error":false,"result":"done"}'` + "\n"} {
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

func TestFinalResultValidation(t *testing.T) {
	init := `printf '{"type":"system","subtype":"init","session_id":"fixture","cwd":"%s"}\n' "$PWD"` + "\n"
	for _, test := range []struct{ name, result, code string }{
		{"reported-error", `{"type":"result","subtype":"error_during_execution","session_id":"fixture","is_error":true,"errors":["failed"]}`, "result_error"},
		{"error-flag", `{"type":"result","subtype":"success","session_id":"fixture","is_error":true}`, "result_error"},
		{"missing-error-flag", `{"type":"result","subtype":"success","session_id":"fixture","result":"done"}`, "invalid_result"},
		{"missing-session", `{"type":"result","subtype":"success","is_error":false}`, "invalid_result"},
		{"negative-usage", `{"type":"result","subtype":"success","session_id":"fixture","is_error":false,"usage":{"output_tokens":-1}}`, "invalid_usage"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, ctx, l, dir := fixture(t, init+"echo '"+test.result+"'\n")
			if _, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"}); err != nil {
				t.Fatal(err)
			}
			if got := finish(t, ctx, a); got.Status != worker.Failed || got.Code != test.code {
				t.Fatalf("%+v", got)
			}
		})
	}
}

func TestInvocationPreservesSetupAndEmitsOneFinish(t *testing.T) {
	body := `test "$*" = '--print --output-format stream-json --verbose' || exit 9
 test "$ANTHROPIC_API_KEY" = fixture-worker-key || exit 10
 test "$(cat "$HOME/.claude/settings.json")" = '{"permissions":{"defaultMode":"default"}}' || exit 11
` + recorded(t, "success") + recorded(t, "success")
	a, ctx, l, dir := fixture(t, body)
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	original := []byte(`{"permissions":{"defaultMode":"default"}}`)
	if err := os.WriteFile(settings, original, 0600); err != nil {
		t.Fatal(err)
	}
	a.cfg.Environment = append(a.cfg.Environment, "HOME="+home, "ANTHROPIC_API_KEY=fixture-worker-key")
	version, err := a.Version(ctx)
	if err != nil || version != "fixture-1" {
		t.Fatalf("version %q %v", version, err)
	}
	if _, err = a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"}); err != nil {
		t.Fatal(err)
	}
	finishes, progress, usages := 0, 0, 0
	for e := range a.Events() {
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
		if e.Finish != nil {
			finishes++
			if e.Finish.Status != worker.Done {
				t.Fatal(e.Finish)
			}
		}
		if e.Progress != nil {
			progress++
		}
		if e.Usage != nil {
			usages++
			if *e.Usage.Input != 4 || *e.Usage.Output != 2 {
				t.Fatal(e.Usage)
			}
		}
	}
	if finishes != 1 || progress != 2 || usages != 1 {
		t.Fatalf("events: finish=%d progress=%d usage=%d", finishes, progress, usages)
	}
	current, err := os.ReadFile(settings)
	if err != nil || string(current) != string(original) {
		t.Fatal("worker settings changed", err)
	}
	files := 0
	if err := filepath.WalkDir(home, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatal("adapter installed files into worker setup")
	}
}

func TestInitializationChecksWorkdirAndModel(t *testing.T) {
	for _, body := range []string{
		`echo '{"type":"system","subtype":"init","session_id":"fixture","cwd":"/other","model":"requested"}'`,
		`printf '{"type":"system","subtype":"init","session_id":"fixture","cwd":"%s","model":"other"}\n' "$PWD"`,
	} {
		a, ctx, l, dir := fixture(t, body+"\n")
		if _, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task", Model: "requested"}); err == nil {
			t.Fatal("mismatched initialization accepted")
		}
	}
}

func TestInformationalMessagesDoNotEndTurn(t *testing.T) {
	stream := recorded(t, "success")
	// A user echo may use a string content, unlike assistant content blocks.
	stream = strings.Replace(stream, `{"type":"assistant"`, `{"type":"user","session_id":"fixture","message":{"content":"task"}}
{"type":"rate_limit_event","session_id":"fixture","rate_limit_info":{"status":"allowed"}}
{"type":"assistant"`, 1)
	a, ctx, l, dir := fixture(t, stream)
	if _, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"}); err != nil {
		t.Fatal(err)
	}
	if got := finish(t, ctx, a); got.Status != worker.Done {
		t.Fatalf("informational message ended turn: %+v", got)
	}
}

func TestFailureSnapshotDistinguishesKnownAndUnknownEffects(t *testing.T) {
	init := `printf '{"type":"system","subtype":"init","session_id":"fixture","cwd":"%s"}\n' "$PWD"` + "\n"
	result := `echo '{"type":"result","subtype":"error_during_execution","session_id":"fixture","is_error":true}'` + "\n"
	for _, tool := range []bool{false, true} {
		t.Run(fmt.Sprint(tool), func(t *testing.T) {
			body := init
			if tool {
				body += `echo '{"type":"assistant","session_id":"fixture","message":{"id":"m","content":[{"type":"tool_use","id":"call","name":"Bash"}]}}'` + "\n"
			}
			a, ctx, l, dir := fixture(t, body+result)
			th, err := a.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "task"})
			if err != nil {
				t.Fatal(err)
			}
			finish(t, ctx, a)
			if err = a.Close(ctx); err != nil {
				t.Fatal(err)
			}
			snap, err := a.Snapshot(ctx, th)
			if err != nil || snap.Finish == nil || snap.Finish.Status != worker.Failed || snap.EffectsKnown == tool {
				t.Fatalf("failure evidence lost: %+v %v", snap, err)
			}
		})
	}
}
