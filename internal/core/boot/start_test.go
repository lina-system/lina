// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/internal/core/ctlfiles"
	"github.com/thisisjun786/lina/kit/protocol"
)

func stateRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "lina-start-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	seedMain(t, root)
	return root
}

type testEvents struct{ trace *[]string }

func (e testEvents) SetEpoch(epoch uint64) error {
	*e.trace = append(*e.trace, fmt.Sprint("event epoch ", epoch))
	return nil
}
func (e testEvents) Close(context.Context) error {
	*e.trace = append(*e.trace, "close events")
	return nil
}

type testCanon struct{ trace *[]string }

func (c testCanon) MirrorEpoch(_ context.Context, epoch uint64) error {
	*c.trace = append(*c.trace, fmt.Sprint("mirror ", epoch))
	return nil
}
func (c testCanon) Close(context.Context) error {
	*c.trace = append(*c.trace, "close canon")
	return nil
}

func TestStartOrderAndDurableEpochBeforeCanon(t *testing.T) {
	root := stateRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var steps []boot.Step
	var trace []string
	cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), PeerSeenMax: 70}
	cfg.Services = boot.Services{
		OpenEvents: func(context.Context) (boot.Events, error) {
			trace = append(trace, "open events null epoch")
			return testEvents{&trace}, nil
		},
		PeekEpoch: func(context.Context) (uint64, error) { trace = append(trace, "peek"); return 80, nil },
		OpenCanon: func(_ context.Context, epoch uint64) (boot.Canon, error) {
			data, err := os.ReadFile(filepath.Join(root, "system/core/epoch"))
			if err != nil || string(data) != "81" || epoch != 81 {
				t.Fatalf("canon opened before durable epoch: %s %d %v", data, epoch, err)
			}
			trace = append(trace, "open canon")
			return testCanon{&trace}, nil
		},
		Reconcile: func(_ context.Context, recovery boot.Recovery) error {
			trace = append(trace, "reconcile")
			if recovery.Epoch != 81 || recovery.MainID != "main-test" {
				t.Fatalf("recovery = %+v", recovery)
			}
			return nil
		},
	}
	cfg.AfterStep = func(step boot.Step, snapshot boot.Snapshot) error {
		steps = append(steps, step)
		if step < boot.EpochMinted && snapshot.Epoch != 0 {
			t.Fatal("authority before epoch mint")
		}
		if step == boot.SocketsBound && snapshot.State != boot.Recovering {
			t.Fatal("socket bound outside recovery")
		}
		if step == boot.Ready {
			if snapshot.State != boot.Running {
				t.Fatal("READY state missing")
			}
			cancel()
		}
		return nil
	}
	if err := boot.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	want := []boot.Step{boot.RootValidated, boot.Locked, boot.EventsOpened, boot.ControlsRead, boot.EpochMinted, boot.CanonOpened, boot.EpochMirrored, boot.SocketPending, boot.SocketCreated, boot.SocketBound, boot.SocketsBound, boot.Reconciled, boot.Ready}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("steps=%v", steps)
	}
	if !reflect.DeepEqual(trace, []string{"open events null epoch", "peek", "event epoch 81", "open canon", "mirror 81", "reconcile", "close canon", "close events"}) {
		t.Fatalf("trace=%v", trace)
	}
}

func TestRecoveringServesHandshakeAndRefusesRequests(t *testing.T) {
	for _, halted := range []bool{false, true} {
		t.Run(fmt.Sprint(halted), func(t *testing.T) {
			root := stateRoot(t)
			socket := filepath.Join(root, "run/core.sock")
			if halted {
				if err := os.WriteFile(filepath.Join(root, "system/core/halt.json"), []byte(`{"reason":"corrupt"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := boot.Config{StateRoot: root, Socket: socket}
			check := func(want protocol.ErrorCode) {
				conn, err := net.Dial("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				request := map[string]any{"jsonrpc": "2.0", "id": "test", "method": "hello", "params": helloEnvelope(t, []string{"1.0"})}
				var response struct {
					Result protocol.Envelope `json:"result"`
				}
				writer, reader := json.NewEncoder(conn), json.NewDecoder(conn)
				if err := writer.Encode(request); err != nil {
					t.Fatal(err)
				}
				if err := reader.Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Result.Kind != protocol.KindWelcome || response.Result.Epoch != "1" {
					t.Fatalf("welcome=%+v", response)
				}
				mutation := request["params"].(protocol.Envelope)
				mutation.Kind, mutation.Payload, mutation.Epoch = protocol.KindInput, protocol.Input{}, response.Result.Epoch
				request["method"], request["params"] = "input", mutation
				if err := writer.Encode(request); err != nil {
					t.Fatal(err)
				}
				if err := reader.Decode(&response); err != nil {
					t.Fatal(err)
				}
				if response.Result.Error == nil || response.Result.Error.Code != want {
					t.Fatalf("answer=%+v", response)
				}
			}
			cfg.AfterStep = func(step boot.Step, _ boot.Snapshot) error {
				if step == boot.SocketsBound {
					check(protocol.ErrorUnavailable)
				}
				if step == boot.Reconciled && halted {
					check(protocol.ErrorUnavailable)
					cancel()
				}
				if step == boot.Ready {
					if halted {
						t.Fatal("halted Core reached READY")
					}
					check(protocol.ErrorCapabilityNotNegotiated)
					cancel()
				}
				return nil
			}
			if err := boot.Run(ctx, cfg); err != nil {
				t.Fatal(err)
			}
			if halted {
				if _, err := os.Stat(filepath.Join(root, "system/core/halt.json")); err != nil {
					t.Fatal("halt marker cleared")
				}
			}
		})
	}
}

func TestFailedStartReleasesLockAndPreservesEpoch(t *testing.T) {
	for _, step := range []boot.Step{boot.EventsOpened, boot.ControlsRead, boot.EpochMinted, boot.CanonOpened, boot.EpochMirrored, boot.SocketPending, boot.SocketCreated, boot.SocketBound, boot.SocketsBound, boot.Reconciled} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			root := stateRoot(t)
			socket := filepath.Join(root, "run/core.sock")
			failure := errors.New("injected startup failure")
			err := boot.Run(context.Background(), boot.Config{StateRoot: root, Socket: socket, AfterStep: func(at boot.Step, _ boot.Snapshot) error {
				if at == step {
					return failure
				}
				return nil
			}})
			if !errors.Is(err, failure) {
				t.Fatalf("error=%v", err)
			}
			store, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			if err := store.Acquire(); err != nil {
				t.Fatal(err)
			}
			epoch, err := store.Epoch()
			if err != nil {
				t.Fatal(err)
			}
			if step >= boot.EpochMinted && epoch != 1 {
				t.Fatalf("epoch lost: %d", epoch)
			}
			if _, err := os.Lstat(socket); !os.IsNotExist(err) {
				t.Fatalf("socket remains: %v", err)
			}
		})
	}
}

// The child uses the real boot implementation. A pipe acknowledges the exact
// completed step; the parent SIGKILLs it while the callback waits on stdin.
func TestCrashChild(t *testing.T) {
	if os.Getenv("LINA_CRASH_CHILD") != "1" {
		return
	}
	// Only this subprocess changes its ambient mask. A bind must be private
	// even when startup inherited the usual, permissive file-creation mask.
	syscall.Umask(0022)
	root := os.Getenv("LINA_CRASH_ROOT")
	target, _ := strconv.Atoi(os.Getenv("LINA_CRASH_STEP"))
	err := boot.Run(context.Background(), boot.Config{StateRoot: root, Socket: os.Getenv("LINA_CRASH_SOCKET"), AfterStep: func(step boot.Step, snapshot boot.Snapshot) error {
		if int(step) == target {
			if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, os.Stdin)
			return err
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func crashAt(t *testing.T, root string, step boot.Step) boot.Snapshot {
	t.Helper()
	return crashAtSocket(t, root, filepath.Join(root, "run/core.sock"), step)
}

func crashAtSocket(t *testing.T, root, socket string, step boot.Step) boot.Snapshot {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestCrashChild$")
	command.Env = append(os.Environ(), "LINA_CRASH_CHILD=1", "LINA_CRASH_ROOT="+root, "LINA_CRASH_SOCKET="+socket, "LINA_CRASH_STEP="+strconv.Itoa(int(step)))
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			command.Process.Kill()
			command.Wait()
		}
	})
	type result struct {
		snapshot boot.Snapshot
		err      error
	}
	received := make(chan result, 1)
	go func() {
		var snapshot boot.Snapshot
		err := json.NewDecoder(bufio.NewReader(output)).Decode(&snapshot)
		received <- result{snapshot, err}
	}()
	var snapshot boot.Snapshot
	select {
	case result := <-received:
		if result.err != nil {
			command.Process.Kill()
			command.Wait()
			waited = true
			t.Fatalf("boundary %d: %v %s", step, result.err, &stderr)
		}
		snapshot = result.snapshot
	case <-time.After(10 * time.Second):
		t.Fatalf("step %d not acknowledged", step)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("child not killed: %v", err)
	}
	return snapshot
}

func TestSIGKILLAtEveryStartStep(t *testing.T) {
	for step := boot.RootValidated; step <= boot.Ready; step++ {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			root := stateRoot(t)
			store, err := ctlfiles.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Acquire(); err != nil {
				t.Fatal(err)
			}
			baseline, err := store.MintEpoch(40, 0)
			if err != nil {
				t.Fatal(err)
			}
			store.Close()
			killed := crashAt(t, root, step)
			restarted := crashAt(t, root, boot.Ready)
			if restarted.Epoch <= max(baseline, killed.Epoch) {
				t.Fatalf("restart epoch=%d, baseline=%d killed=%d", restarted.Epoch, baseline, killed.Epoch)
			}
		})
	}
}

func TestBadControlRecordsStopBeforeEpochMint(t *testing.T) {
	for _, tc := range []struct{ name, data string }{
		{"main.json", `{}`}, {"main.json", `null`}, {"main.json", `{"main_id":" "}`},
		{"run.sentinel", `{"epoch":"01","socket":"/unused"}`}, {"run.sentinel", `{}`},
		{"run.sentinel", `{"epoch":"1"}`}, {"run.sentinel", `{"epoch":"1","socket":null}`},
		{"run.sentinel", `{"epoch":"1","socket":"","pending_socket":"relative.sock"}`},
		{"run.sentinel", `{"epoch":"1","socket":"","pending_socket":null}`},
		{"run.sentinel", `{"epoch":"1","socket":"","pending_socket":42}`},
		{"halt.json", `broken`}, {"lifecycle.json", `null`},
	} {
		t.Run(tc.name+tc.data, func(t *testing.T) {
			root := stateRoot(t)
			if err := os.WriteFile(filepath.Join(root, "system/core", tc.name), []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := runToReady(t, root, filepath.Join(root, "run/core.sock")); err == nil {
				t.Fatal("bad control record accepted")
			}
			if _, err := os.Stat(filepath.Join(root, "system/core/epoch")); !os.IsNotExist(err) {
				t.Fatalf("epoch minted: %v", err)
			}
		})
	}
}

func TestExistingCanonNeedsReadOnlyPeek(t *testing.T) {
	root := stateRoot(t)
	if err := os.Mkdir(filepath.Join(root, "canon"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "canon/store.db"), []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	err := boot.Run(context.Background(), boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock")})
	if err == nil || !strings.Contains(err.Error(), "canon service") {
		t.Fatalf("ignored existing canon: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system/core/epoch")); !os.IsNotExist(err) {
		t.Fatalf("epoch minted without peek: %v", err)
	}
}

func TestRecoveryReceivesPreviousControls(t *testing.T) {
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	previous := []byte(fmt.Sprintf(`{"epoch":"5","socket":%q}`, socket))
	for name, data := range map[string][]byte{"run.sentinel": previous, "lifecycle.json": []byte(`{"mode":"when_idle"}`), "epoch": []byte("5")} {
		if err := os.WriteFile(filepath.Join(root, "system/core", name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := boot.Config{StateRoot: root, Socket: socket, Services: boot.Services{Reconcile: func(_ context.Context, recovery boot.Recovery) error {
		if recovery.Previous == nil || recovery.Previous.Epoch != "5" || recovery.Previous.Socket != socket || string(recovery.Lifecycle) != `{"mode":"when_idle"}` || recovery.Epoch != 6 {
			t.Fatalf("lost previous controls: %+v", recovery)
		}
		return nil
	}}, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
		if step == boot.Ready {
			cancel()
		}
		return nil
	}}
	if err := boot.Run(ctx, cfg); err != nil {
		t.Fatal(err)
	}
}

func TestEpochFailureNeverOpensCanon(t *testing.T) {
	for _, value := range []string{"broken", "18446744073709551615"} {
		t.Run(value, func(t *testing.T) {
			root := stateRoot(t)
			if err := os.WriteFile(filepath.Join(root, "system/core/epoch"), []byte(value), 0600); err != nil {
				t.Fatal(err)
			}
			opened := false
			cfg := boot.Config{StateRoot: root, Socket: filepath.Join(root, "run/core.sock"), Services: boot.Services{
				PeekEpoch: func(context.Context) (uint64, error) { return 1, nil },
				OpenCanon: func(context.Context, uint64) (boot.Canon, error) {
					opened = true
					return nil, errors.New("should not open")
				},
			}}
			if err := boot.Run(context.Background(), cfg); err == nil || opened {
				t.Fatalf("error=%v canon opened=%v", err, opened)
			}
			saved, err := os.ReadFile(filepath.Join(root, "system/core/epoch"))
			if err != nil || string(saved) != value {
				t.Fatalf("epoch changed: %s %v", saved, err)
			}
		})
	}
}
