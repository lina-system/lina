// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2OutsideRecordsRefreshInstalledCore(t *testing.T) {
	t.Parallel()
	h, start := installedConversationHarness(t, "http://127.0.0.1:1")
	core := start(false)
	defer h.stopCore(core)
	observer, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "outside-records", []protocol.Capability{{Name: "progress", Version: "1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	topic := "progress"
	if _, err = observer.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	frame, err := observer.Receive(h.ctx)
	if _, ok := frame.Payload.(protocol.Snapshot); err != nil || frame.Error != nil || !ok {
		t.Fatalf("subscription: %+v %v", frame, err)
	}
	// Change only the filesystem: no Core request or canon mutation can wake it.
	path := filepath.Join(h.root, "versions", "records", "outside.md")
	if err = os.WriteFile(path, []byte("unaccepted outside content"), 0600); err != nil {
		t.Fatal(err)
	}
	awaitOutside := func(want bool) {
		t.Helper()
		ctx, cancel := context.WithTimeout(h.ctx, operationTimeout)
		defer cancel()
		for {
			frame, err := observer.Receive(ctx)
			if err != nil || frame.Error != nil {
				t.Fatalf("outside=%t progress update: %+v %v", want, frame, err)
			}
			update, ok := frame.Payload.(protocol.ProgressUpdate)
			if !ok || update.Waiting == nil {
				continue
			}
			found := false
			for _, row := range *update.Waiting {
				found = found || row.Ref.Source == "outside"
			}
			if found == want {
				return
			}
		}
	}
	awaitOutside(true)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	awaitOutside(false)
}
