// SPDX-License-Identifier: Apache-2.0
package boot_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestCoreProcess(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/lina")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	for _, signal := range []os.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(signal.String(), func(t *testing.T) {
			root, err := os.MkdirTemp("", "lina-core-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(root) })
			socket := filepath.Join(root, "run", "core.sock")
			seedMain(t, root)
			for iteration := range 2 {
				epoch := strconv.Itoa(iteration + 1)
				child := startCore(t, binary, root, socket)
				idle := child.connect(t, socket) // Readiness is a successful connection.
				for _, versions := range [][]string{{"1.0"}, {"2.0", "1.0"}, {"2.0", "1.1"}} {
					conn := child.connect(t, socket)
					conn.SetDeadline(time.Now().Add(5 * time.Second))
					hello := helloEnvelope(t, versions)
					request := struct {
						JSONRPC string            `json:"jsonrpc"`
						ID      string            `json:"id"`
						Method  string            `json:"method"`
						Params  protocol.Envelope `json:"params"`
					}{"2.0", "handshake", "hello", hello}
					if err := json.NewEncoder(conn).Encode(request); err != nil {
						t.Fatal(err)
					}
					reader := bufio.NewReader(conn)
					line, err := reader.ReadBytes('\n')
					if err != nil {
						t.Fatalf("handshake: %v", err)
					}
					var response struct {
						JSONRPC string            `json:"jsonrpc"`
						ID      string            `json:"id"`
						Result  protocol.Envelope `json:"result"`
					}
					if err := json.Unmarshal(line, &response); err != nil {
						t.Fatalf("decode response: %v\n%s", err, line)
					}
					envelope := response.Result
					if response.JSONRPC != "2.0" || response.ID != "handshake" || envelope.ReplyTo == nil || *envelope.ReplyTo != hello.MessageID {
						t.Fatalf("uncorrelated response: %s", line)
					}
					if envelope.Sender != (protocol.Sender{Party: protocol.PartyCore, ID: "main-test"}) || envelope.Epoch != epoch {
						t.Fatalf("unexpected identity: %s", line)
					}
					if versions[len(versions)-1] == "1.0" {
						welcome, ok := envelope.Payload.(protocol.Welcome)
						want := protocol.Welcome{
							ProtocolVersion: "1.0", MainID: "main-test", Epoch: epoch,
							PersonID: "person-placeholder", Agents: []protocol.Agent{}, Capabilities: []protocol.Capability{},
						}
						if !ok || envelope.Kind != protocol.KindWelcome || envelope.Error != nil || envelope.EffectState != protocol.EffectNone || !reflect.DeepEqual(welcome, want) {
							t.Fatalf("unexpected welcome: %s", line)
						}
						// Handshake is served during RECOVERING. Probe until READY's
						// explicit refusal, under the original connection deadline.
						for attempt := 0; ; attempt++ {
							request.Params.MessageID = "after-welcome-" + strconv.Itoa(attempt)
							request.Params.AttemptID = request.Params.MessageID
							if err := json.NewEncoder(conn).Encode(request); err != nil {
								t.Fatal(err)
							}
							line, err := reader.ReadBytes('\n')
							if err != nil {
								t.Fatal(err)
							}
							if err := json.Unmarshal(line, &response); err != nil {
								t.Fatal(err)
							}
							answer := response.Result
							if response.JSONRPC != "2.0" || response.ID != request.ID || answer.ReplyTo == nil || *answer.ReplyTo != request.Params.MessageID || answer.Epoch != epoch || answer.Sender != envelope.Sender || answer.EffectState != protocol.EffectRefused || answer.Error == nil {
								t.Fatalf("unexpected post-handshake answer: %s", line)
							}
							if answer.Error.Code == protocol.ErrorCapabilityNotNegotiated {
								break
							}
							if answer.Error.Code != protocol.ErrorUnavailable {
								t.Fatalf("unexpected post-handshake answer: %s", line)
							}
						}
					} else {
						if envelope.Kind != protocol.KindError || envelope.Error == nil || envelope.Error.Code != protocol.ErrorUnsupportedCombination || envelope.EffectState != protocol.EffectRefused {
							t.Fatalf("unexpected refusal: %s", line)
						}
						if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
							t.Fatalf("refusal must close connection: %v", err)
						}
					}
					conn.Close()
				}
				// Inspect after a round trip, so binding and chmod have both completed.
				assertMode(t, filepath.Dir(socket), 0700, os.ModeDir)
				assertMode(t, socket, 0600, os.ModeSocket)
				assertMode(t, filepath.Join(root, "system/core"), 0700, os.ModeDir)
				assertMode(t, filepath.Join(root, "system/core/epoch"), 0600, 0)
				child.stop(t, signal)
				if _, err := os.Lstat(socket); !os.IsNotExist(err) {
					t.Fatalf("socket remains after shutdown: %v", err)
				}
				idle.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := idle.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatalf("idle connection after shutdown: %v", err)
				}
				idle.Close()
				if _, err := os.Stat(filepath.Join(root, "system/core/run.sentinel")); !os.IsNotExist(err) {
					t.Fatalf("sentinel remains after shutdown: %v", err)
				}
			}
		})
	}
}

func helloEnvelope(t *testing.T, versions []string) protocol.Envelope {
	t.Helper()
	data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	hello := envelope.Payload.(protocol.Hello)
	hello.ProtocolVersions = versions
	hello.Capabilities = []protocol.Capability{{Name: "conversation", Version: "1.0"}}
	envelope.Payload = hello
	return envelope
}

type coreChild struct {
	command *exec.Cmd
	done    chan struct{}
	err     error
	output  bytes.Buffer
}

func startCore(t *testing.T, binary, root, socket string) *coreChild {
	t.Helper()
	child := &coreChild{command: exec.Command(binary, "core", "--state-root", root, "--socket", socket), done: make(chan struct{})}
	child.command.Env = []string{"HOME=" + root, "XDG_STATE_HOME=" + root, "XDG_RUNTIME_DIR=" + root}
	child.command.Dir = root
	child.command.Stdout, child.command.Stderr = &child.output, &child.output
	if err := child.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { child.err = child.command.Wait(); close(child.done) }()
	t.Cleanup(func() { child.command.Process.Kill(); <-child.done })
	return child
}

func (child *coreChild) connect(t *testing.T, socket string) net.Conn {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	retry := time.NewTicker(5 * time.Millisecond)
	defer retry.Stop()
	for {
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return conn
		}
		select {
		case <-child.done:
			t.Fatalf("Core exited before accepting connections: %v\n%s", child.err, &child.output)
		case <-deadline.C:
			t.Fatalf("Core did not accept connections: %v", err)
		case <-retry.C:
		}
	}
}

func (child *coreChild) stop(t *testing.T, signal os.Signal) {
	t.Helper()
	if err := child.command.Process.Signal(signal); err != nil {
		t.Fatal(err)
	}
	select {
	case <-child.done:
		if child.err != nil {
			t.Fatalf("shutdown: %v\n%s", child.err, &child.output)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Core did not stop")
	}
}

func assertMode(t *testing.T, path string, mode os.FileMode, kind os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode || info.Mode().Type() != kind {
		t.Fatalf("mode of %s = %s", path, info.Mode())
	}
}

func seedMain(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-test"}`), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSecondCoreExits69(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/lina")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	root := stateRoot(t)
	socket := filepath.Join(root, "run/core.sock")
	child := startCore(t, binary, root, socket)
	connection := child.connect(t, socket)
	defer connection.Close()
	before, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	second := exec.Command(binary, "core", "--state-root", root, "--socket", socket)
	output, err := second.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 69 {
		t.Fatalf("second Core: %v\n%s", err, output)
	}
	after, err := os.Stat(socket)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("second Core replaced socket: %v", err)
	}
	epoch, err := os.ReadFile(filepath.Join(root, "system/core/epoch"))
	if err != nil || string(epoch) != "1" {
		t.Fatalf("second Core changed epoch: %s %v", epoch, err)
	}
	child.stop(t, syscall.SIGTERM)
}
