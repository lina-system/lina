// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"bufio"
	"context"
	"encoding/json"
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
	if os.Getenv("ACP_FIXTURE") == "1" {
		agentFixture()
		return
	}
	os.Exit(m.Run())
}

type testLeaser struct{}

func (testLeaser) Acquire(context.Context, lease.Activity) (lease.Lease, error) {
	return lease.New(func() {}), nil
}

func fixture(t *testing.T) (*Adapter, context.Context, lease.Lease, worker.StartSpec) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	l := lease.New(func() {})
	a := New(Config{Profile: Profile{Name: "added", Command: exe}, Environment: []string{"ACP_FIXTURE=1", "LINA_RESPONSES_API_KEY=must-not-leak", "WORKER_LOGIN=preserved"}, Epoch: 7, Leaser: testLeaser{}})
	t.Cleanup(func() {
		cancel()
		if err := a.Close(context.Background()); err != nil {
			t.Error(err)
		}
		l.Release()
	})
	return a, ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "success"}
}
func event(t *testing.T, ctx context.Context, a *Adapter) worker.Event {
	t.Helper()
	select {
	case e, ok := <-a.Events():
		if !ok {
			t.Fatal("closed event stream")
		}
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
		return e
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return worker.Event{}
	}
}
func terminal(t *testing.T, ctx context.Context, a *Adapter) worker.Finish {
	t.Helper()
	for {
		e := event(t, ctx, a)
		if e.Finish != nil {
			return *e.Finish
		}
	}
}
func TestPromptLifecycle(t *testing.T) {
	a, ctx, l, s := fixture(t)
	th, err := a.Start(ctx, l, s)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for {
		e := event(t, ctx, a)
		if e.Thread != th {
			t.Fatalf("unbound event: %+v", e)
		}
		if e.Progress != nil && e.Progress.Text == "Writing result" {
			seen = true
		}
		if e.Finish != nil {
			if e.Finish.Status != worker.Done || !seen {
				t.Fatalf("finish before progress: %+v", e)
			}
			break
		}
	}
	snap, err := a.Snapshot(ctx, th)
	if err != nil || snap.State != worker.Idle || snap.Version != "fixture-9" || snap.Workdir != s.Workdir || snap.Output != "Writing result" {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	b, err := os.ReadFile(filepath.Join(s.Workdir, "result.txt"))
	if err != nil || string(b) != "result\n" {
		t.Fatalf("result %q %v", b, err)
	}
	if err = a.Input(ctx, th, worker.Input{Text: "exit"}); err != nil {
		t.Fatal(err)
	}
	f := terminal(t, ctx, a)
	if f.Status != worker.Failed || f.Failure != worker.ConnectionFailure {
		t.Fatalf("exit became success: %+v", f)
	}
}
func TestCancelWaitsForReply(t *testing.T) {
	a, ctx, l, s := fixture(t)
	s.TaskPacket = "cancel"
	th, err := a.Start(ctx, l, s)
	if err != nil {
		t.Fatal(err)
	}
	for {
		e := event(t, ctx, a)
		if e.Progress != nil && e.Progress.Text == "Waiting" {
			break
		}
	}
	stale := th
	stale.BindingEpoch++
	if err = a.Interrupt(ctx, stale, worker.UserCancel); err == nil {
		t.Fatal("stale cancel accepted")
	}
	if err = a.Input(ctx, th, worker.Input{Text: "steer"}); !strings.Contains(fmt.Sprint(err), "refused") {
		t.Fatalf("active input not refused: %v", err)
	}
	if err = a.Interrupt(ctx, th, worker.UserCancel); err != nil {
		t.Fatal(err)
	}
	// Agent acknowledges seeing cancel in progress, then blocks on an explicit
	// filesystem barrier. No elapsed interval is evidence of interruption.
	for {
		e := event(t, ctx, a)
		if e.Finish != nil {
			t.Fatal("interrupted without cancelled reply")
		}
		if e.Progress != nil && e.Progress.Text == "Cancel received" {
			break
		}
	}
	snap, err := a.Snapshot(ctx, th)
	if err != nil || snap.State != worker.Active {
		t.Fatalf("early terminal state: %+v %v", snap, err)
	}
	if err = os.WriteFile(filepath.Join(s.Workdir, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if f := terminal(t, ctx, a); f.Status != worker.Interrupted || f.Code != "cancelled" {
		t.Fatal(f)
	}
}
func TestAllStopReasonsAndWorkerCancel(t *testing.T) {
	for _, reason := range []string{"refusal", "max_tokens", "max_turn_requests", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			a, ctx, l, s := fixture(t)
			s.TaskPacket = reason
			if _, err := a.Start(ctx, l, s); err != nil {
				t.Fatal(err)
			}
			f := terminal(t, ctx, a)
			if reason == "cancelled" {
				if f.Status != worker.Interrupted || f.Code != "worker_cancelled" {
					t.Fatal(f)
				}
			} else if f.Status != worker.Failed || f.Code != reason || f.Failure != worker.WorkerFailure {
				t.Fatal(f)
			}
		})
	}
}
func TestInvalidStartDoesNotLaunch(t *testing.T) {
	a, ctx, l, s := fixture(t)
	s.Required = worker.CapabilitySet{worker.WorkTools}
	if _, err := a.Start(ctx, l, s); err == nil {
		t.Fatal("unsupported capability accepted")
	}
	s.Required = nil
	s.Workdir = "relative"
	if _, err := a.Start(ctx, l, s); err == nil {
		t.Fatal("relative workdir accepted")
	}
}
func agentFixture() {
	if os.Getenv("LINA_RESPONSES_API_KEY") != "" || os.Getenv("WORKER_LOGIN") != "preserved" {
		os.Exit(10)
	}
	scan := bufio.NewScanner(os.Stdin)
	send := func(v any) {
		if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
			os.Exit(11)
		}
	}
	reply := func(id json.RawMessage, result any) {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	progress := func(text string) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": text}}}})
	}
	var pending json.RawMessage
	for scan.Scan() {
		var f struct {
			ID     json.RawMessage
			Method string
			Params struct {
				Cwd    string
				Prompt []struct{ Text string }
			}
		}
		if json.Unmarshal(scan.Bytes(), &f) != nil {
			os.Exit(12)
		}
		switch f.Method {
		case "initialize":
			reply(f.ID, map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "fixture", "version": "fixture-9"}})
		case "session/new":
			cwd, _ := os.Getwd()
			if f.Params.Cwd != cwd {
				os.Exit(13)
			}
			reply(f.ID, map[string]any{"sessionId": "fixture"})
		case "session/prompt":
			text := f.Params.Prompt[0].Text
			switch text {
			case "exit":
				os.Exit(0)
			case "unknown":
				pending = f.ID
				send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture", "update": map[string]any{"sessionUpdate": "future_result", "status": "done"}}})
				progress("Waiting")
			case "cancel":
				pending = f.ID
				progress("Waiting")
			case "success":
				progress("Writing result")
				os.WriteFile("result.txt", []byte("result\n"), 0600)
				reply(f.ID, map[string]any{"stopReason": "end_turn"})
			default:
				reply(f.ID, map[string]any{"stopReason": text})
			}
		case "session/cancel":
			progress("Cancel received")
			for {
				if _, err := os.Stat("release"); err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			reply(pending, map[string]any{"stopReason": "cancelled"})
		default:
			os.Exit(14)
		}
	}
}

func TestSnapshotFindsNextTurnWithoutAuthorizingStaleControl(t *testing.T) {
	a, ctx, l, s := fixture(t)
	th, err := a.Start(ctx, l, s)
	if err != nil {
		t.Fatal(err)
	}
	if f := terminal(t, ctx, a); f.Status != worker.Done {
		t.Fatal(f)
	}
	if err = a.Input(ctx, th, worker.Input{Text: "cancel"}); err != nil {
		t.Fatal(err)
	}
	snap, err := a.Snapshot(ctx, th)
	if err != nil || snap.Thread.TurnID == th.TurnID || snap.State != worker.Active {
		t.Fatalf("cannot discover next turn: %+v %v", snap, err)
	}
	if err = a.Interrupt(ctx, th, worker.UserCancel); err == nil {
		t.Fatal("stale turn authorized cancel")
	}
	foreign := snap.Thread
	foreign.ProcessGeneration++
	if _, err = a.Snapshot(ctx, foreign); err == nil {
		t.Fatal("foreign process snapshot accepted")
	}
}

func TestUnknownUpdateIsEvidenceNotCompletion(t *testing.T) {
	a, ctx, l, s := fixture(t)
	s.TaskPacket = "unknown"
	th, err := a.Start(ctx, l, s)
	if err != nil {
		t.Fatal(err)
	}
	for {
		e := event(t, ctx, a)
		if e.Finish != nil {
			t.Fatal("unknown update ended turn")
		}
		if e.Progress != nil && e.Progress.Text == "Waiting" {
			break
		}
	}
	snap, err := a.Snapshot(ctx, th)
	if err != nil || snap.State != worker.Active {
		t.Fatalf("unknown update changed state: %+v %v", snap, err)
	}
	result := a.Result()
	if len(result.Unknown) != 1 || !strings.Contains(string(result.Unknown[0]), `"future_result"`) {
		t.Fatal("unknown wire JSON lost", result)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = a.Interrupt(cancelled, th, worker.UserCancel); err == nil {
		t.Fatal("cancelled control context was sent")
	}
}

func TestUnverifiedVersionIsReportedBeforeWorkAndStillRuns(t *testing.T) {
	a, ctx, l, s := fixture(t)
	if _, err := a.Start(ctx, l, s); err != nil {
		t.Fatal(err)
	}
	first := event(t, ctx, a)
	if first.Progress == nil || first.Progress.Phase != "compatibility" || !strings.Contains(first.Progress.Text, "fixture-9") || !strings.Contains(first.Progress.Text, "unverified") {
		t.Fatalf("missing readiness notice: %+v", first.Progress)
	}
	if f := terminal(t, ctx, a); f.Status != worker.Done {
		t.Fatal("unverified version refused", f)
	}
}
