// SPDX-License-Identifier: Apache-2.0
package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
)

func TestPromptRPCErrorIsWorkerFailure(t *testing.T) {
	for _, mode := range []string{"rpc-error", "rpc-error-exit"} {
		t.Run(mode, func(t *testing.T) {
			a, ctx, l, spec := fixture(t)
			spec.TaskPacket = mode
			thread, err := a.Start(ctx, l, spec)
			if err != nil {
				t.Fatal(err)
			}
			finish := terminal(t, ctx, a)
			if finish.Status != worker.Failed || finish.Failure != worker.WorkerFailure || finish.Code != "prompt_failed" {
				t.Fatalf("observed RPC error misclassified: %+v", finish)
			}
			raw, err := json.Marshal(a.Result())
			if err != nil || strings.Contains(string(raw), "private-") {
				t.Fatalf("remote error detail retained: %s %v", raw, err)
			}
			if mode == "rpc-error-exit" {
				return
			}
			snapshot, err := a.Snapshot(ctx, thread)
			if err != nil || snapshot.State != worker.Idle {
				t.Fatalf("RPC error killed the session: %+v %v", snapshot, err)
			}
			if err = a.Input(ctx, thread, worker.Input{Text: "success"}); err != nil {
				t.Fatal(err)
			}
			if next := terminal(t, ctx, a); next.Status != worker.Done {
				t.Fatalf("next prompt failed: %+v", next)
			}
		})
	}
}
