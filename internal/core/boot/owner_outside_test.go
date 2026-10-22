// SPDX-License-Identifier: Apache-2.0
package boot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/records"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestOwnerViewRefreshObservesOutsideEditAndRestore(t *testing.T) {
	r := controlRuntime(t)
	r.ownerViews.stop()
	<-r.ownerViews.done
	data := []byte(fmt.Sprintf("---\nid: \"outside-fixture\"\nkind: memo\nstate: active\nperson_id: %q\nagent_id: %q\n---\n\n# Fixture\n", r.person, r.agent))
	path := "inbox/progress-fixture.md"
	if _, err := r.records.Commit(t.Context(), records.Change{ID: "outside-fixture", Path: path, Data: data}, records.Actor{Person: r.person, Agent: r.agent}); err != nil {
		t.Fatal(err)
	}
	_, sub, err := r.topics.Subscribe("progress", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ticks := make(chan time.Time, 1)
	watch, cancel := context.WithCancel(t.Context())
	r.ownerViews.stop = cancel
	r.ownerViews.done = make(chan struct{})
	r.ownerViews.last = ""
	go func() { defer close(r.ownerViews.done); r.runOwnerViews(watch, ticks) }()
	t.Cleanup(func() { cancel(); <-r.ownerViews.done })
	next := func(want int) {
		t.Helper()
		ctx, stop := context.WithTimeout(t.Context(), 3*time.Second)
		defer stop()
		frame, err := sub.Next(ctx)
		if err != nil {
			t.Fatalf("outside refresh did not arrive: %v", err)
		}
		var p protocol.ProgressUpdate
		if err = json.Unmarshal(frame.Event.Data, &p); err != nil {
			t.Fatal(err)
		}
		if p.Waiting == nil || len(*p.Waiting) != want {
			t.Fatalf("outside rows=%+v want=%d", p.Waiting, want)
		}
		if want > 0 && (*p.Waiting)[0].Ref.Source != "outside" {
			t.Fatalf("wrong source: %+v", p.Waiting)
		}
	}
	next(0)
	changed := r.db.Changed()
	fullPath := filepath.Join(r.root, "versions/records", path)
	if err = os.WriteFile(fullPath, append(append([]byte{}, data...), []byte("Outside edit\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	next(1)
	select {
	case <-changed:
		t.Fatal("test relied on a canon mutation")
	default:
	}
	if err = os.WriteFile(fullPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Now()
	next(0)
	select {
	case <-changed:
		t.Fatal("restore relied on a canon mutation")
	default:
	}
}
