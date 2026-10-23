// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

func TestStartRetainsTurnCompletedBeforeReply(t *testing.T) {
	for _, status := range []string{"completed", "failed"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer inR.Close()
			defer inW.Close()
			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			s := &Session{stdin: inW, cancel: cancel, readerDone: make(chan struct{}),
				events: make(chan worker.Event, 257), pending: map[string]chan frame{},
				finished: map[string]bool{}, generation: 1, version: "fixture"}
			go s.read(outR)
			defer func() {
				s.mu.Lock()
				s.closing = true
				s.mu.Unlock()
				outW.Close()
				<-s.readerDone
			}()
			deadline, _ := ctx.Deadline()
			if err := inR.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
			scan := bufio.NewScanner(inR)
			read := func(method string) frame {
				t.Helper()
				if !scan.Scan() {
					t.Fatal("missing request", method, scan.Err())
				}
				var request frame
				if err := json.Unmarshal(scan.Bytes(), &request); err != nil || request.Method != method {
					t.Fatalf("request=%s err=%v", scan.Bytes(), err)
				}
				return request
			}
			send := func(value any) {
				t.Helper()
				if err := json.NewEncoder(outW).Encode(value); err != nil {
					t.Fatal(err)
				}
			}
			activity := lease.New(func() {})
			defer activity.Release()
			cwd := t.TempDir()
			type result struct {
				thread worker.Thread
				err    error
			}
			started := make(chan result, 1)
			go func() {
				thread, err := s.Start(ctx, activity, worker.StartSpec{TaskID: "task", AttemptID: "attempt", Workdir: cwd, TaskPacket: "Complete the task."})
				started <- result{thread, err}
			}()
			request := read("thread/start")
			send(map[string]any{"id": request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread"}, "cwd": cwd, "model": "fixture"}})
			request = read("turn/start")
			// No turn/started notification: wait until the reader has processed
			// completion before releasing the reply, with no scheduling sleeps.
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thread", "turn": map[string]any{"id": "turn", "status": status}}})
			var completed worker.Event
			select {
			case completed = <-s.Events():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if completed.Kind != worker.FinishEvent || completed.Thread.TurnID != "turn" {
				t.Fatalf("completion=%+v", completed)
			}
			send(map[string]any{"id": request.ID, "result": map[string]any{"turn": map[string]any{"id": "turn", "status": "inProgress"}}})
			select {
			case got := <-started:
				if got.err != nil || got.thread != completed.Thread {
					t.Fatalf("start binding=%+v completion binding=%+v err=%v", got.thread, completed.Thread, got.err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			s.mu.Lock()
			state := s.snapshot.State
			s.mu.Unlock()
			if state != worker.Idle {
				t.Fatalf("completed turn state=%s", state)
			}
		})
	}
}
