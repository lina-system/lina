// SPDX-License-Identifier: Apache-2.0
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/worker"
)

// Observing Done acknowledges that the operation reached its cancellable wait.
// The watchdog only fails a stuck test; elapsed time never establishes success.
type observedControlContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *observedControlContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.observed) })
	return c.Context.Done()
}

func TestSnapshotCancellationDuringControl(t *testing.T) {
	for _, method := range []string{"turn/steer", "turn/interrupt"} {
		t.Run(method, func(t *testing.T) {
			inR, inW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer inR.Close()
			defer inW.Close()
			thread := worker.Thread{ID: "thread", TurnID: "turn", TaskID: "task", AttemptID: "attempt", ProcessGeneration: 1, BindingEpoch: 1}
			s := &Session{stdin: inW, readerDone: make(chan struct{}), pending: map[string]chan frame{}, snapshot: worker.ThreadSnapshot{Thread: thread, State: worker.Active}}
			controlDone := make(chan error, 1)
			go func() {
				if method == "turn/steer" {
					controlDone <- s.Input(t.Context(), thread, worker.Input{Text: "keep working"})
				} else {
					controlDone <- s.Interrupt(t.Context(), thread, worker.UserCancel)
				}
			}()
			var request frame
			if err := json.NewDecoder(inR).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request.Method != method {
				t.Fatalf("control request = %s, want %s", request.Method, method)
			}
			// The real control RPC is now waiting for its reply while owning the gate.
			release := sync.OnceFunc(func() {
				s.mu.Lock()
				s.pending[string(request.ID)] <- frame{Result: json.RawMessage(`{"turnId":"turn"}`)}
				s.mu.Unlock()
				if err := <-controlDone; err != nil {
					t.Errorf("held control: %v", err)
				}
			})
			defer release()
			watchdog, stop := context.WithTimeout(t.Context(), 3*time.Second)
			defer stop()
			for _, operation := range []string{"snapshot", "input", "recover", "reconcile"} {
				ctx, cancel := context.WithCancel(t.Context())
				observed := &observedControlContext{Context: ctx, observed: make(chan struct{})}
				var result error
				done := make(chan struct{})
				go func() {
					defer close(done)
					switch operation {
					case "snapshot":
						_, result = s.Snapshot(observed, thread)
					case "recover":
						_, result = s.Recover(observed, thread, "/fixture", nil)
					case "reconcile":
						_, result = s.Reconcile(observed, thread)
					case "input":
						result = s.Input(observed, thread, worker.Input{Text: "queued input"})
					}
				}()
				// Always release and join, including with the original blocking mutex.
				defer func() { cancel(); release(); <-done }()
				select {
				case <-observed.observed:
				case <-watchdog.Done():
					t.Fatal("snapshot lock wait did not observe cancellation")
				}
				cancel()
				select {
				case <-done:
					if !errors.Is(result, context.Canceled) || operation == "input" && !errors.Is(result, worker.ErrInputRefused) {
						t.Fatalf("%s cancellation = %v", operation, result)
					}
				case <-watchdog.Done():
					t.Fatalf("%s waited for the held control after cancellation", operation)
				}
			}
			s.mu.Lock()
			writes := s.next
			s.mu.Unlock()
			if writes != 1 {
				t.Fatalf("conflicting operations sent %d RPCs while control was held", writes)
			}
		})
	}
}
