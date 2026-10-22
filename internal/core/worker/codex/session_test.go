// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
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
	if os.Getenv("LINA_CODEX_FIXTURE") != "" && len(os.Args) > 1 {
		if os.Args[1] == "--version" {
			fmt.Println("codex-cli 99.0.0")
			return
		}
		if os.Args[1] == "app-server" {
			fixture()
			return
		}
	}
	os.Exit(m.Run())
}

func fixture() {
	if !reflect.DeepEqual(os.Args[1:], []string{"app-server", "--listen", "stdio://"}) {
		os.Exit(81)
	}
	if os.Getenv("OPENAI_API_KEY") != "worker-own-key" || os.Getenv("LINA_RESPONSES_API_KEY") != "" {
		os.Exit(82)
	}
	scanner := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if enc.Encode(v) != nil {
			os.Exit(83)
		}
	}
	cwd, turn, status := "", "", "idle"
	initialized := false
	n := 0
	for scanner.Scan() {
		var r struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
		}
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			os.Exit(84)
		}
		reply := func(v any) { send(map[string]any{"id": r.ID, "result": v}) }
		notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
		switch r.Method {
		case "initialize":
			var p struct {
				Capabilities struct {
					ExperimentalAPI bool `json:"experimentalApi"`
				}
			}
			json.Unmarshal(scanner.Bytes(), new(any))
			b, _ := json.Marshal(r.Params)
			json.Unmarshal(b, &p)
			if !p.Capabilities.ExperimentalAPI {
				os.Exit(85)
			}
			reply(map[string]any{"userAgent": "fixture/99.0.0", "codexHome": os.Getenv("CODEX_HOME"), "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
			initialized = true
		case "account/rateLimits/read":
			rate := map[string]any{"primary": map[string]any{"usedPercent": 100, "resetsAt": time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Unix()}}
			reply(map[string]any{"rateLimits": rate})
			notify("account/rateLimits/updated", map[string]any{"rateLimits": rate})
		case "thread/start":
			if !initialized || len(r.Params) != 1 {
				os.Exit(86)
			}
			json.Unmarshal(r.Params["cwd"], &cwd)
			reply(map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd, "status": map[string]any{"type": "idle"}}, "cwd": cwd, "model": "own-model", "reasoningEffort": "medium", "instructionSources": []string{"AGENTS.md"}})
		case "turn/start":
			if len(r.Params) != 2 {
				os.Exit(87)
			}
			n++
			turn = fmt.Sprintf("turn-%d", n)
			status = "active"
			if os.Getenv("LINA_CODEX_FIXTURE") != "pending-start" {
				notify("turn/started", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turn, "status": "inProgress"}})
			} else {
				status = "idle"
			}
			if n == 2 {
				notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": turn, "itemId": "message", "delta": "result"})
				status = "idle"
				notify("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turn, "status": "completed"}})
			}
			reply(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress"}})
		case "turn/steer":
			var expected string
			json.Unmarshal(r.Params["expectedTurnId"], &expected)
			if expected != turn || status != "active" {
				os.Exit(88)
			}
			reply(map[string]any{"turnId": turn})
		case "turn/interrupt":
			var id string
			json.Unmarshal(r.Params["turnId"], &id)
			if id != turn {
				os.Exit(89)
			}
			status = "idle"
			notify("turn/completed", map[string]any{"threadId": "thread-1", "turn": map[string]any{"id": turn, "status": "interrupted"}})
			reply(map[string]any{})
		case "thread/read":
			if _, present := r.Params["includeTurns"]; present {
				os.Exit(91)
			}
			switch os.Getenv("LINA_CODEX_FIXTURE") {
			case "cancel-read":
				notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": turn, "itemId": "message", "delta": "read_pending"})
				continue
			case "overflow":
				for i := 0; i < 300; i++ {
					notify("item/agentMessage/delta", map[string]any{"threadId": "thread-1", "turnId": turn, "itemId": "message", "delta": "x"})
				}
				continue
			case "oversize":
				fmt.Println(strings.Repeat("x", maxFrame+1))
				continue
			}
			if os.Getenv("LINA_CODEX_FIXTURE") == "eof" {
				return
			}
			if os.Getenv("LINA_CODEX_FIXTURE") == "malformed" {
				fmt.Println("not-json")
				return
			}
			turns := []any{}
			if turn != "" && os.Getenv("LINA_CODEX_FIXTURE") != "pending-start" {
				s := "completed"
				if status == "active" {
					s = "inProgress"
				}
				turns = append(turns, map[string]any{"id": turn, "status": s})
			}
			reply(map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd, "status": map[string]any{"type": status}, "turns": turns}})
		default:
			os.Exit(90)
		}
	}
}

func openFixture(t *testing.T, mode string) (*Session, context.Context, lease.Lease) {
	t.Helper()
	t.Setenv("LINA_CODEX_FIXTURE", mode)
	t.Setenv("OPENAI_API_KEY", "worker-own-key")
	t.Setenv("LINA_RESPONSES_API_KEY", "core-only-key")
	t.Setenv("CODEX_HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	l := lease.New(func() {})
	t.Cleanup(l.Release)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, l, Config{Executable: exe})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s, ctx, l
}

func finish(t *testing.T, ctx context.Context, s *Session, want worker.FinishStatus) worker.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("events closed before finish")
			}
			if err := e.Validate(); err != nil {
				t.Fatal(err)
			}
			if e.Kind == worker.FinishEvent {
				if e.Finish.Status != want {
					s.mu.Lock()
					cause := s.failure
					s.mu.Unlock()
					t.Fatalf("finish=%+v cause=%v", e.Finish, cause)
				}
				return e
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestSessionRoundTripAndSettings(t *testing.T) {
	s, ctx, l := openFixture(t, "normal")
	v, err := s.Version(ctx)
	if err != nil || v != "99.0.0" {
		t.Fatalf("version=%q %v", v, err)
	}
	if s.Compatibility().Verified || s.Compatibility().Version != v {
		t.Fatal("unverified version not reported")
	}
	if err := s.Capabilities().Require(worker.StreamProgress, worker.SignalCompletion, worker.SteerTurn); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	thread, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: dir, TaskPacket: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != worker.Active || snap.Version != v || snap.Workdir != dir || snap.Model != "own-model" {
		t.Fatalf("snapshot=%+v", snap)
	}
	thread = snap.Thread
	for _, mutate := range []func(*worker.Thread){func(x *worker.Thread) { x.ProcessGeneration++ }, func(x *worker.Thread) { x.BindingEpoch++ }, func(x *worker.Thread) { x.TurnID = "stale" }, func(x *worker.Thread) { x.TaskID = "foreign" }} {
		stale := thread
		mutate(&stale)
		if err := s.Input(ctx, stale, worker.Input{Text: "must refuse"}); err == nil {
			t.Fatal("stale input accepted")
		}
	}
	if err := s.Input(ctx, thread, worker.Input{Text: "steer"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(ctx, thread, worker.Reason("invalid")); err == nil {
		t.Fatal("invalid interrupt accepted")
	}
	if err := s.Interrupt(ctx, thread, worker.UserCancel); err != nil {
		t.Fatal(err)
	}
	finish(t, ctx, s, worker.Interrupted)
	snap, err = s.Snapshot(ctx, thread)
	if err != nil || snap.State != worker.Idle {
		t.Fatalf("idle: %+v %v", snap, err)
	}
	if err := s.Input(ctx, snap.Thread, worker.Input{Text: "finish"}); err != nil {
		t.Fatal(err)
	}
	finish(t, ctx, s, worker.Done)
	snap, err = s.Snapshot(ctx, thread)
	if err != nil || snap.State != worker.Idle {
		t.Fatalf("completion raced reply: %+v %v", snap, err)
	}
	if _, err = s.Start(ctx, l, worker.StartSpec{Workdir: dir, TaskPacket: "duplicate"}); err == nil {
		t.Fatal("second task accepted")
	}
}

func TestSessionConnectionFailure(t *testing.T) {
	for _, mode := range []string{"eof", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, l := openFixture(t, mode)
			th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "hold"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Snapshot(ctx, th); err == nil {
				t.Fatal("bad transport accepted")
			}
			e := finish(t, ctx, s, worker.Failed)
			if e.Finish.Failure != worker.ConnectionFailure {
				t.Fatalf("failure=%+v", e.Finish)
			}
		})
	}
}

func TestInstalledCodex(t *testing.T) {
	if os.Getenv("LINA_CODEX_LIVE") != "1" {
		t.Skip("explicit installed-worker acceptance only")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	l := lease.New(func() {})
	defer l.Release()
	s, err := Open(ctx, l, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, _ := s.Version(ctx)
	t.Logf("installed Codex %s verified=%t", v, s.Compatibility().Verified)
	dir := t.TempDir()
	th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "acceptance", AttemptID: "1", Workdir: dir, TaskPacket: "Wait for further instructions. Do not use tools yet."})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.Events():
		if e.Kind != worker.ProgressEvent || e.Progress.Phase != "started" {
			t.Fatalf("expected turn start: %+v", e)
		}
		th = e.Thread
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err = s.Interrupt(ctx, th, worker.UserCancel); err != nil {
		t.Fatal(err)
	}

	finish(t, ctx, s, worker.Interrupted)
	snap, err := s.Snapshot(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Input(ctx, snap.Thread, worker.Input{Text: "Reply with exactly LINA_CODEX_OK. Do not use any tools."}); err != nil {
		t.Fatal(err)
	}
	var result string
loop:
	for {
		select {
		case e, ok := <-s.Events():
			if !ok {
				t.Fatal("session closed")
			}
			if e.Progress != nil && e.Progress.Phase == "item_completed" && e.Progress.Text != "" {
				result = e.Progress.Text
			}
			if e.Finish != nil {
				if e.Finish.Status != worker.Done {
					t.Fatalf("finish=%+v", e.Finish)
				}
				break loop
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if result != "LINA_CODEX_OK" {
		t.Fatalf("result mismatch: %q", result)
	}
	snap, err = s.Snapshot(ctx, th)
	if err != nil || snap.State != worker.Idle {
		t.Fatalf("final snapshot state=%s %v", snap.State, err)
	}
	t.Log("initialize, start, interrupt, idle input, model result and snapshot passed")
}

func TestCanceledRPCDoesNotInterruptWorker(t *testing.T) {
	s, ctx, l := openFixture(t, "cancel-read")
	th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Snapshot(call, th); done <- err }()
	for {
		e := <-s.Events()
		if e.Progress != nil && e.Progress.Text == "read_pending" {
			break
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("canceled read=%v", err)
	}
	if err := s.Input(ctx, th, worker.Input{Text: "still active"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Interrupt(ctx, th, worker.UserCancel); err != nil {
		t.Fatal(err)
	}
	finish(t, ctx, s, worker.Interrupted)
}

func TestEventOverflowAndOversizedFrameAreObservable(t *testing.T) {
	for _, mode := range []string{"overflow", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			s, ctx, l := openFixture(t, mode)
			th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "hold"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Snapshot(ctx, th); err == nil {
				t.Fatal("lost observations accepted")
			}
			e := finish(t, ctx, s, worker.Failed)
			if e.Finish.Failure != worker.ConnectionFailure {
				t.Fatal(e.Finish)
			}
		})
	}
}

func TestUnknownRequiredCapabilityAndWorkerSettingsAreRefusedBeforeAssignment(t *testing.T) {
	s, ctx, l := openFixture(t, "normal")
	base := worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "hold"}
	for _, capability := range []worker.Capability{worker.Capability("future-capability")} {
		spec := base
		spec.Required = worker.CapabilitySet{capability}
		if _, err := s.Start(ctx, l, spec); err == nil {
			t.Fatalf("unsupported %s assigned", capability)
		}
	}
	if _, err := s.Start(ctx, lease.Lease{}, base); err == nil {
		t.Fatal("invalid lease assigned")
	}
	if _, err := s.Start(ctx, l, base); err != nil {
		t.Fatal(err)
	}
}

func TestSummarySnapshotDoesNotReopenAcceptedPendingTurn(t *testing.T) {
	s, ctx, l := openFixture(t, "pending-start")
	th, err := s.Start(ctx, l, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: t.TempDir(), TaskPacket: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, th)
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != worker.Unknown {
		t.Fatalf("accepted turn with idle summary must await reconciliation, got %s", snap.State)
	}
	if err := s.Input(ctx, snap.Thread, worker.Input{Text: "must not open another turn"}); err == nil {
		t.Fatal("pending accepted turn was restarted")
	}
}
