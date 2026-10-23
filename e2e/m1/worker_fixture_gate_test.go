// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// A real worker remains responsive to snapshots while its work is pending.
// Force two reads before completion so fixture correctness cannot depend on
// whether Core's observer or worker_start asks for a snapshot first.
func TestM2WorkerFixtureFinishGate(t *testing.T) {
	t.Parallel()
	release, entered := make(chan struct{}), make(chan struct{}, 1)
	var launches atomic.Int32
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		launches.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer gate.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), operationTimeout)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, exe, "app-server", "--listen", "stdio://")
	command.Env = []string{"LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_WORKER_FINISH_GATE=" + gate.URL}
	in, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); in.Close(); command.Wait() }()
	type message struct {
		ID     int
		Method string
	}
	messages := make(chan message)
	go func() {
		defer close(messages)
		decoder := json.NewDecoder(out)
		for {
			var m message
			if decoder.Decode(&m) != nil {
				return
			}
			select {
			case messages <- m:
			case <-ctx.Done():
				return
			}
		}
	}()
	encoder := json.NewEncoder(in)
	send := func(id int, method string, params any) {
		t.Helper()
		if err := encoder.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			t.Fatal(err)
		}
	}
	wait := func(id int, method string) {
		t.Helper()
		for {
			select {
			case m, ok := <-messages:
				if !ok {
					t.Fatal("fixture closed before expected response")
				}
				if id != 0 && m.ID == id || method != "" && m.Method == method {
					return
				}
			case <-ctx.Done():
				t.Fatalf("fixture blocked pending response id=%d method=%q while completion was gated: %v", id, method, ctx.Err())
			}
		}
	}
	send(1, "initialize", nil)
	wait(1, "")
	send(0, "initialized", nil)
	workdir := t.TempDir()
	send(2, "thread/start", map[string]string{"cwd": workdir})
	wait(2, "")
	send(3, "turn/start", map[string]any{"threadId": "fixture-thread", "input": []map[string]string{{"text": "create a result"}}})
	wait(3, "")
	send(4, "thread/read", map[string]string{"threadId": "fixture-thread"})
	wait(4, "")
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker did not start the completion gate")
	}
	send(5, "thread/read", map[string]string{"threadId": "fixture-thread"})
	wait(5, "")
	close(release)
	wait(0, "turn/completed")
	if raw, err := os.ReadFile(filepath.Join(workdir, "result.txt")); err != nil || string(raw) != "LINA_WORKER_RESULT" || launches.Load() != 1 {
		t.Fatalf("completion bytes=%q launches=%d error=%v", raw, launches.Load(), err)
	}
}
