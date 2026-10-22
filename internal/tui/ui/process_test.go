// SPDX-License-Identifier: Apache-2.0

package ui_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestTUIProcess(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "lina")
	build := exec.Command("go", "build", "-mod=readonly", "-o", binary, "../../../cmd/lina")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	t.Run("live and quit", func(t *testing.T) {
		root := socketRoot(t)
		socket := filepath.Join(root, "core.sock")
		if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"main-tui-test"}`), 0600); err != nil {
			t.Fatal(err)
		}
		core := exec.Command(binary, "core", "--state-root", root, "--socket", socket)
		core.Env = []string{"HOME=" + root, "XDG_STATE_HOME=" + root}
		var coreOutput bytes.Buffer
		core.Stdout, core.Stderr = &coreOutput, &coreOutput
		if err := core.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { core.Wait(); close(done) }()
		t.Cleanup(func() { core.Process.Kill(); <-done })
		// Core boot includes the SQLite WASM runtime and migrations. Give that
		// startup its own bound; success still requires an observed live socket
		// and the TUI handshake below, not elapsed time.
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		retry := time.NewTicker(5 * time.Millisecond)
		defer retry.Stop()
		for {
			conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
			if err == nil {
				conn.Close()
				break
			}
			select {
			case <-done:
				t.Fatalf("Core stopped: %s", &coreOutput)
			case <-deadline.C:
				t.Fatalf("Core not ready: %v", err)
			case <-retry.C:
			}
		}
		for _, input := range []string{"q", ""} {
			t.Run("input="+input, func(t *testing.T) {
				cmd := tuiCommand(t, binary, root, "--socket", socket)
				stdin, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				stdout, err := cmd.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				// Wait for the handshake result before sending input. Keeping stdin
				// open after q proves that quit does not depend on EOF or a newline.
				line, err := bufio.NewReader(stdout).ReadString('\n')
				if err != nil || strings.TrimSpace(line) != "live" {
					cmd.Process.Kill()
					cmd.Wait()
					t.Fatalf("connection status = %q, %v; stderr: %s", line, err, &stderr)
				}
				if input == "" {
					stdin.Close()
				} else if _, err := io.WriteString(stdin, input); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("quit: %v; stderr: %s", err, &stderr)
				}
				if stderr.Len() != 0 {
					t.Fatalf("unexpected diagnostic: %s", &stderr)
				}
				conn, err := net.Dial("unix", socket)
				if err != nil {
					t.Fatalf("TUI stopped Core: %v", err)
				}
				conn.Close()
			})
		}
	})
	t.Run("absent socket", func(t *testing.T) {
		root := socketRoot(t)
		cmd := tuiCommand(t, binary, root, "--socket", filepath.Join(root, "missing", "core.sock"))
		checkExit(t, cmd, 69, "unavailable")
		entries, err := os.ReadDir(root)
		if err != nil || len(entries) != 0 {
			t.Fatalf("TUI created state or a socket: %v, %v", entries, err)
		}
	})
	t.Run("unsupported combination", func(t *testing.T) {
		root := socketRoot(t)
		socket := filepath.Join(root, "refused.sock")
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		served := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				served <- err
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			var request struct {
				ID     string            `json:"id"`
				Params protocol.Envelope `json:"params"`
			}
			if err := json.NewDecoder(conn).Decode(&request); err != nil {
				served <- err
				return
			}
			data, err := os.ReadFile("../../../protocol/fixtures/client/1.0/unsupported_combination.json")
			if err != nil {
				served <- err
				return
			}
			var refusal protocol.Envelope
			if err := json.Unmarshal(data, &refusal); err != nil {
				served <- err
				return
			}
			refusal.ReplyTo = &request.Params.MessageID
			// Untrusted detail must not become terminal control sequences.
			refusal.Error.Detail = "\x1b]52;c;payload\a"
			served <- json.NewEncoder(conn).Encode(struct {
				JSONRPC string            `json:"jsonrpc"`
				ID      string            `json:"id"`
				Result  protocol.Envelope `json:"result"`
			}{"2.0", request.ID, refusal})
		}()
		checkExit(t, tuiCommand(t, binary, root, "--socket", socket), 78, "unsupported_combination")
		if err := <-served; err != nil {
			t.Fatal(err)
		}
	})
	for _, args := range [][]string{nil, {"--socket="}, {"--unknown"}, {"--socket", "unused", "extra"}} {
		t.Run("usage="+strings.Join(args, " "), func(t *testing.T) {
			checkExit(t, tuiCommand(t, binary, socketRoot(t), args...), 64, "usage:")
		})
	}
}

func socketRoot(t *testing.T) string {
	t.Helper()
	// Unix socket paths have a small platform limit; avoid the long test name.
	root, err := os.MkdirTemp("", "lina-tui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	return root
}

func tuiCommand(t *testing.T, binary, root string, args ...string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, append([]string{"tui"}, args...)...)
	cmd.Env = []string{"HOME=" + root, "XDG_STATE_HOME=" + root, "PATH="}
	cmd.Dir = root
	return cmd
}

func checkExit(t *testing.T, cmd *exec.Cmd, code int, diagnostic string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("exit = %v, want %d; stderr: %s", err, code, &stderr)
	}
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), diagnostic) || strings.ContainsAny(stderr.String(), "\x1b\a") {
		t.Fatalf("stdout=%q stderr=%q", &stdout, &stderr)
	}
}
