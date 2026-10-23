// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/secret"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

type streamedProgressAdapter struct {
	quotaStopAdapter
	phase  string
	chunks []string
}

func (a *streamedProgressAdapter) Name() string { return "stream-fixture" }
func (a *streamedProgressAdapter) Start(ctx context.Context, l lease.Lease, s worker.StartSpec) (worker.Thread, error) {
	th, err := a.quotaStopAdapter.Start(ctx, l, s)
	<-a.events
	for _, chunk := range a.chunks {
		a.events <- worker.Event{Thread: th, Kind: worker.ProgressEvent, Progress: &worker.Progress{Phase: a.phase, ItemID: "message", Text: chunk}}
	}
	a.events <- worker.Event{Thread: th, Kind: worker.FinishEvent, Finish: &worker.Finish{Status: worker.Interrupted, Code: "cancelled"}}
	return th, err
}
func TestDispatchMasksStreamedCredentialsBeforeLedger(t *testing.T) {
	for _, phase := range []string{"message", "thought", "delta"} {
		t.Run(phase, func(t *testing.T) {
			s, r, _, _, _ := switchDispatchFixture(t, false)
			suffix := strings.Repeat("a", 20)
			a := &streamedProgressAdapter{quotaStopAdapter: quotaStopAdapter{events: make(chan worker.Event, 8)}, phase: phase, chunks: []string{"Working: sk-", suffix, " done"}}
			r.Definition.Worker = a.Name()
			if err := s.save(t.Context(), r); err != nil {
				t.Fatal(err)
			}
			s.cfg.Workers = []Factory{{Name: a.Name(), Open: func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
				return a, func(context.Context) error { return nil }, nil
			}}}
			s.execute(s.ctx, r)
			events, err := s.cfg.Work.Ledger(t.Context(), r.ID)
			if err != nil {
				t.Fatal(err)
			}
			var recorded strings.Builder
			count := 0
			for _, e := range events {
				if e.Kind == "worker_progress" {
					recorded.WriteString(e.Value)
					count++
				}
			}
			if count == 0 {
				t.Fatal("all progress was discarded")
			}
			if strings.Contains(recorded.String(), suffix) {
				t.Fatal("streamed credential suffix reached the durable progress ledger")
			}
		})
	}
}

// Advance a logical clock per rune to exercise every possible split even when
// persistence is permitted between fragments. No scheduler timing is involved.
func TestProgressBufferMasksEveryStreamBoundary(t *testing.T) {
	for name, input := range map[string]string{
		"key":        "Preparing sk-" + strings.Repeat("a", 20) + " done",
		"github":     "Preparing ghp_" + strings.Repeat("a", 20) + " done",
		"assignment": "Preparing token=syntheticcredential done",
		"bearer":     "Authorization: Bearer syntheticcredential done",
		"quoted":     `Preparing password="synthetic pass phrase" done`,
		"json":       `{"api_key":"syntheticcredential","stage":"done"}`,
		"pem":        "Preparing -----BEGIN PRIVATE KEY-----\nsyntheticcredential\n-----END PRIVATE KEY----- done",
	} {
		t.Run(name, func(t *testing.T) {
			var b progressBuffer
			at := time.Unix(1, 0)
			var observations strings.Builder
			for _, r := range input {
				at = at.Add(progressInterval)
				text, err := b.Observe(worker.Progress{Phase: "message", Text: string(r)}, at)
				if err != nil {
					t.Fatal(err)
				}
				observations.WriteString(text)
				// Even a prefix of an assignment or quoted value must stay private.
				for _, private := range []string{strings.Repeat("a", 8), "synthetic", "pass phrase"} {
					if strings.Contains(text, private) {
						t.Fatal("partial credential reached progress")
					}
				}
			}
			text, err := b.Flush(at, true)
			if err != nil {
				t.Fatal(err)
			}
			observations.WriteString(text)
			for _, private := range []string{strings.Repeat("a", 8), "synthetic", "pass phrase"} {
				if strings.Contains(text, private) {
					t.Fatal("credential reached final progress")
				}
			}
			if !strings.Contains(observations.String(), secret.Placeholder) {
				t.Fatal("masked progress was never projected")
			}
		})
	}
}

func TestProgressBufferCoalescesAndFlushesWhileRunning(t *testing.T) {
	var b progressBuffer
	at := time.Unix(1, 0)
	text, err := b.Observe(worker.Progress{Phase: "compatibility", Text: "Unverified version"}, at)
	if err != nil || text == "" {
		t.Fatalf("initial observation: %q %v", text, err)
	}
	for range 1000 {
		text, err = b.Observe(worker.Progress{Phase: "message", Text: "work "}, at)
		if err != nil || text != "" {
			t.Fatalf("token emitted an uncoalesced write: %q %v", text, err)
		}
	}
	text, err = b.Flush(at.Add(progressInterval), false)
	if err != nil || text == "" || len([]rune(text)) > 513 {
		t.Fatalf("active timer observation: %q %v", text, err)
	}
	if again, err := b.Flush(at.Add(2*progressInterval), false); err != nil || again != "" {
		t.Fatalf("unchanged observation repeated: %q %v", again, err)
	}
	_, _ = b.Observe(worker.Progress{Phase: "message", Text: "done"}, at.Add(progressInterval))
	if text, err = b.Flush(at.Add(progressInterval), true); err != nil || !strings.HasSuffix(text, "done") {
		t.Fatalf("terminal observation: %q %v", text, err)
	}
}

func TestProgressBufferBoundsRetainedText(t *testing.T) {
	var b progressBuffer
	at := time.Unix(1, 0)
	_, err := b.Observe(worker.Progress{Phase: "message", Text: "token=" + strings.Repeat("x", progressTextLimit)}, at)
	if err != nil {
		t.Fatal(err)
	}
	if b.latest.raw.Len() != 0 || !b.latest.overflow {
		t.Fatal("oversized stream retained raw text")
	}
	text, err := b.Observe(worker.Progress{Phase: "message", Text: "private remainder"}, at.Add(progressInterval))
	if err != nil || strings.Contains(text, "remainder") || b.latest.raw.Len() != 0 {
		t.Fatal("overflow lost its private context")
	}
}

func TestProgressBufferWithholdsUnfinishedQuotedValueAtFinish(t *testing.T) {
	for _, text := range []string{
		`Working password="syntheticcredential`,
		`{"api_key":"syntheticcredential`,
	} {
		var b progressBuffer
		at := time.Unix(1, 0)
		if _, err := b.Observe(worker.Progress{Phase: "message", Text: text}, at); err != nil {
			t.Fatal(err)
		}
		final, err := b.Flush(at, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(final, "syntheticcredential") {
			t.Fatal("unfinished credential reached terminal progress")
		}
	}
}

func TestProgressBufferWhitespaceBeforeStructuredValue(t *testing.T) {
	var b progressBuffer
	at := time.Unix(1, 0)
	if text, err := b.Observe(worker.Progress{Phase: "message", Text: "\n"}, at); err != nil || text != "" {
		t.Fatalf("whitespace observation: %q %v", text, err)
	}
	if text, err := b.Observe(worker.Progress{Phase: "message", Text: `{"stage":"working"}`}, at.Add(progressInterval)); err != nil || text != "" {
		t.Fatalf("active structured observation: %q %v", text, err)
	}
	if text, err := b.Flush(at.Add(progressInterval), true); err != nil || !strings.Contains(text, "working") {
		t.Fatalf("complete structured observation: %q %v", text, err)
	}
}
