// SPDX-License-Identifier: Apache-2.0
//go:build storebench_core

package storebench

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/kit/protocol"
)

// Opt in under dv heavy. The normal Core owns startup, migrations, recovery,
// socket and memory handlers; there is no benchmark-only production endpoint.
func TestHeadlessCore(t *testing.T) {
	root, err := os.MkdirTemp("", "lina-sb-core-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	r, err := measure(t.Context(), root, generate(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "system/core"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "system/core/main.json"), []byte(`{"main_id":"storebench"}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	ready, done := make(chan struct{}), make(chan struct{})
	var runErr error
	socket := filepath.Join(root, "core.sock")
	go func() {
		runErr = boot.Run(ctx, boot.Config{StateRoot: root, Socket: socket, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step == boot.Ready {
				close(ready)
			}
			return nil
		}})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
			if runErr != nil {
				t.Errorf("Core shutdown: %v", runErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("Core shutdown deadline")
		}
	})
	select {
	case <-ready:
	case <-done:
		t.Fatalf("Core startup: %v", runErr)
	case <-ctx.Done():
		t.Fatal("Core readiness deadline")
	}
	client, err := clientapi.Dial(ctx, socket, "0.0.0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if client.Welcome.PersonID != person || len(client.Welcome.Agents) == 0 {
		t.Fatal("Core identity missing")
	}
	a := client.Welcome.Agents[0].ID
	request := func(operation string, id *string) protocol.MemoryResult {
		t.Helper()
		requestID, err := client.Memory(ctx, protocol.MemoryRequest{Operation: &operation, AgentID: &a, ItemID: id})
		if err != nil {
			t.Fatal(err)
		}
		for {
			envelope, err := client.Receive(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if envelope.ReplyTo == nil || *envelope.ReplyTo != requestID {
				continue
			}
			if envelope.Error != nil {
				t.Fatalf("memory request refused: %s", envelope.Error.Code)
			}
			result, ok := envelope.Payload.(protocol.MemoryResult)
			if !ok {
				t.Fatal("missing memory result")
			}
			return result
		}
	}
	listed := request("list", nil)
	if listed.Items == nil || len(*listed.Items) != r.MemoryItems-r.ForgetHidden {
		t.Fatal("Core did not recover the benchmark's visible memory")
	}
	id := (*listed.Items)[0].ItemID
	request("forget", &id)
	listed = request("list", nil)
	if listed.Items == nil || len(*listed.Items) != 0 {
		t.Fatal("Core forget did not hide the remaining benchmark item")
	}
}
