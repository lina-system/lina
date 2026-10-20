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
			for range 2 { // A restart keeps the placeholders and needs no control files.
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
					if envelope.Sender != (protocol.Sender{Party: protocol.PartyCore, ID: "main-placeholder"}) || envelope.Epoch != "0" {
						t.Fatalf("unexpected identity: %s", line)
					}
					if versions[len(versions)-1] == "1.0" {
						welcome, ok := envelope.Payload.(protocol.Welcome)
						want := protocol.Welcome{
							ProtocolVersion: "1.0", MainID: "main-placeholder", Epoch: "0",
							PersonID: "person-placeholder", Agents: []protocol.Agent{}, Capabilities: []protocol.Capability{},
						}
						if !ok || envelope.Kind != protocol.KindWelcome || envelope.Error != nil || envelope.EffectState != protocol.EffectNone || !reflect.DeepEqual(welcome, want) {
							t.Fatalf("unexpected welcome: %s", line)
						}
						// A welcomed connection remains usable, but grants no operations.
						request.Params.MessageID = "after-welcome"
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
						if response.Result.Error == nil || response.Result.Error.Code != protocol.ErrorCapabilityNotNegotiated {
							t.Fatalf("unexpected post-handshake answer: %s", line)
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
				assertFiles(t, root, []string{"run", "run/core.sock"})
				child.stop(t, signal)
				idle.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := idle.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
					t.Fatalf("idle connection after shutdown: %v", err)
				}
				idle.Close()
				assertFiles(t, root, []string{"run"})
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
	child := &coreChild{command: exec.Command(binary, "core", "--socket", socket), done: make(chan struct{})}
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

func assertFiles(t *testing.T, root string, want []string) {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root {
			name, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			found = append(found, name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(found, want) {
		t.Fatalf("files = %v, want %v (no state or control files)", found, want)
	}
}
