// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2ProgressWaitingInstalledCore(t *testing.T) {
	t.Parallel()
	var fixture struct {
		OutsideText string `json:"outside_text"`
	}
	data, err := os.ReadFile("../m2/progress-waiting.json")
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.OutsideText == "" {
		t.Fatal("missing outside-change fixture", err)
	}
	runCodexWorkTools(t, "../m2/progress-waiting.json", func(h *harness, phase string) {
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

		if phase == "restarted" {
			// Keep the existing subscription open. Filesystem-only changes must arrive
			// without another query, conversation turn or canon mutation to wake Core.
			path := filepath.Join(h.root, "versions", "records", "inbox", "progress-outside.md")
			if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			defer os.Remove(path)
			waitOutside := func(want bool) {
				t.Helper()
				ctx, cancel := context.WithTimeout(h.ctx, 15*time.Second)
				defer cancel()
				for {
					e, err := c.Receive(ctx)
					if err != nil || e.Error != nil {
						t.Fatalf("outside change not published: %v %+v", err, e.Error)
					}
					update, ok := e.Payload.(protocol.ProgressUpdate)
					if !ok || update.Waiting == nil {
						continue
					}
					found := false
					for _, row := range *update.Waiting {
						found = found || row.Ref.Source == "outside"
					}
					raw, _ := json.Marshal(update)
					if strings.Contains(string(raw), fixture.OutsideText) {
						t.Fatal("unaccepted record contents reached the view")
					}
					if found == want {
						return
					}
				}
			}
			if err = os.WriteFile(path, []byte(fixture.OutsideText), 0600); err != nil {
				t.Fatal(err)
			}
			waitOutside(true)
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			waitOutside(false)
		}
	})
}
