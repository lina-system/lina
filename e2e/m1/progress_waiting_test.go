// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2ProgressWaitingInstalledCore(t *testing.T) {
	t.Parallel()
	runCodexWorkTools(t, "../m2/progress-waiting.json", "", func(h *harness, phase string) {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "progress-reader", []protocol.Capability{{Name: "progress", Version: "1.0"}, {Name: "waiting", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		mode := "overview"
		if _, err = c.Progress(h.ctx, protocol.ProgressGet{View: &mode}); err != nil {
			t.Fatal(err)
		}
		frame, err := c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("progress unavailable: %v %+v", err, frame.Error)
		}
		p, ok := frame.Payload.(protocol.Progress)
		if !ok || p.Rows == nil {
			t.Fatalf("missing progress snapshot: %+v", frame.Payload)
		}
		if phase != "restarted" {
			if len(*p.Rows) != 1 {
				t.Fatalf("%s live worker absent from progress: %+v", phase, p)
			}
			line := (*p.Rows)[0].Line
			if line.Stage != "unknown" || line.Percent != nil {
				t.Fatalf("invented plan/verification: %+v", line)
			}
			raw, _ := json.Marshal(line)
			for _, hidden := range []string{"work-tools-thread", "work-tools-turn", "pull_request", "\"id\"", "\"ref\""} {
				if strings.Contains(string(raw), hidden) {
					t.Fatalf("internal reference in line: %s", raw)
				}
			}
		}
		if _, err = c.Waiting(h.ctx, protocol.WaitingGet{}); err != nil {
			t.Fatal(err)
		}
		frame, err = c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("waiting unavailable: %v %+v", err, frame.Error)
		}
		list, ok := frame.Payload.(protocol.Waiting)
		if !ok || list.Rows == nil {
			t.Fatalf("missing full waiting list: %+v", frame.Payload)
		}
		want := 0
		source := ""
		if phase == "approval" {
			want = 1
			source = "approval"
		}
		if phase == "question" {
			want = 1
			source = "worker"
		}
		if len(*list.Rows) != want {
			t.Fatalf("%s waiting source not current: %+v", phase, list)
		}
		if want > 0 && (*list.Rows)[0].Ref.Source != source {
			t.Fatalf("wrong producer: %+v", list)
		}
		topic := "progress"
		if _, err = c.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
			t.Fatal(err)
		}
		frame, err = c.Receive(h.ctx)
		if err != nil || frame.Error != nil {
			t.Fatalf("progress topic unavailable: %v %+v", err, frame.Error)
		}
		snapshot, ok := frame.Payload.(protocol.Snapshot)
		if !ok || len(snapshot.Items) != 1 {
			t.Fatalf("progress subscription: %+v", frame.Payload)
		}
	})
}
