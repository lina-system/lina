// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tuiterm "github.com/thisisjun786/lina/internal/tui/term"
)

type panelFrame struct {
	at     time.Time
	screen tuiterm.Snapshot
}

// Arm before injecting a transition. The PTY reader records the first complete
// renderer write that contains it, even if the test goroutine wakes much later.
type panelFrameProbe struct {
	mu    sync.Mutex
	match func(tuiterm.Snapshot) bool
	found chan panelFrame
}

func (p *panelFrameProbe) arm(match func(tuiterm.Snapshot) bool) <-chan panelFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.match, p.found = match, make(chan panelFrame, 1)
	return p.found
}
func (p *panelFrameProbe) observe(at time.Time, s tuiterm.Snapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.match != nil && p.match(s) {
		p.found <- panelFrame{at, s}
		p.match = nil
	}
}

func TestWorkPanelFirstFrameMeasurement(t *testing.T) {
	screen, err := tuiterm.NewRecorder(20, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer screen.Close()
	probe := &panelFrameProbe{}
	writer := &terminalScreenWriter{screen: screen, observe: probe.observe}
	frame := func(text string) []byte { return []byte("\x1b[H" + text + string(terminalWriteEnd)) }
	found := probe.arm(func(s tuiterm.Snapshot) bool { return strings.HasPrefix(screenText(s), "NEW") })
	writer.Write(frame("OLD"))
	raw := frame("NEW")
	writer.Write(raw[:len(raw)-1])
	select {
	case <-found:
		t.Fatal("partial frame counted")
	default:
	}
	before := time.Now()
	writer.Write(append(raw[len(raw)-1:], frame("NEWER")...))
	after := time.Now()
	select {
	case got := <-found:
		if got.at.Before(before) || got.at.After(after) || !strings.HasPrefix(screenText(got.screen), "NEW ") {
			t.Fatalf("did not retain first complete frame: %s at %s", screenText(got.screen), got.at)
		}
	default:
		t.Fatal("first frame was not recorded by the PTY writer")
	}
	select {
	case <-found:
		t.Fatal("later frame replaced first observation")
	default:
	}
}

// The runtime E8 driver will supply one ledger event and its first matched
// frame per transition. A missing/duplicate sample must never improve p95.
type panelLatencySample struct {
	event     string
	committed time.Time
	frame     panelFrame
}

type panelLatencyBudget struct {
	Count int `json:"transitions"`
	P95MS int `json:"p95_ms"`
	MaxMS int `json:"max_ms"`
}

func panelLatencySummary(samples []panelLatencySample, budget panelLatencyBudget) (p95, maximum time.Duration, err error) {
	if budget.Count <= 0 || len(samples) != budget.Count || budget.P95MS <= 0 || budget.MaxMS < budget.P95MS {
		return 0, 0, fmt.Errorf("incomplete latency sample set or invalid budget")
	}
	durations := make([]time.Duration, 0, len(samples))
	seen := map[string]bool{}
	for _, s := range samples {
		if s.event == "" || seen[s.event] || s.committed.IsZero() || s.frame.at.IsZero() || s.frame.at.Before(s.committed) {
			return 0, 0, fmt.Errorf("invalid latency sample for event %q", s.event)
		}
		seen[s.event] = true
		durations = append(durations, s.frame.at.Sub(s.committed))
	}
	slices.Sort(durations)
	p95, maximum = durations[(95*len(durations)+99)/100-1], durations[len(durations)-1]
	if p95 > time.Duration(budget.P95MS)*time.Millisecond || maximum > time.Duration(budget.MaxMS)*time.Millisecond {
		err = fmt.Errorf("latency exceeds budget: p95=%s max=%s", p95, maximum)
	}
	return
}

func TestWorkPanelLatencyBudget(t *testing.T) {
	raw, err := os.ReadFile("../m3/work-panel-latency.json")
	if err != nil {
		t.Fatal(err)
	}
	var budget panelLatencyBudget
	if err = json.Unmarshal(raw, &budget); err != nil {
		t.Fatal(err)
	}
	if budget.Count != 50 || budget.P95MS != 500 || budget.MaxMS != 1000 {
		t.Fatal("T-16 E8 workload or budget changed")
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := func() []panelLatencySample {
		samples := make([]panelLatencySample, budget.Count)
		for i := range samples {
			samples[i] = panelLatencySample{fmt.Sprint(i), base, panelFrame{at: base.Add(100 * time.Millisecond)}}
		}
		return samples
	}
	for _, tc := range []struct {
		name   string
		change func([]panelLatencySample) []panelLatencySample
	}{
		{"missing sample", func(s []panelLatencySample) []panelLatencySample { return s[:49] }},
		{"extra sample", func(s []panelLatencySample) []panelLatencySample { return append(s, s[0]) }},
		{"duplicate event", func(s []panelLatencySample) []panelLatencySample { s[1].event = s[0].event; return s }},
		{"missing commit", func(s []panelLatencySample) []panelLatencySample { s[0].committed = time.Time{}; return s }},
		{"missing frame", func(s []panelLatencySample) []panelLatencySample { s[0].frame.at = time.Time{}; return s }},
		{"frame before commit", func(s []panelLatencySample) []panelLatencySample {
			s[0].frame.at = base.Add(-time.Nanosecond)
			return s
		}},
		{"p95 over budget", func(s []panelLatencySample) []panelLatencySample {
			for i := 0; i < 3; i++ {
				s[i].frame.at = base.Add(500*time.Millisecond + time.Nanosecond)
			}
			return s
		}},
		{"max over budget", func(s []panelLatencySample) []panelLatencySample {
			s[0].frame.at = base.Add(time.Second + time.Nanosecond)
			return s
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := panelLatencySummary(tc.change(fresh()), budget); err == nil {
				t.Fatal("invalid measurement accepted")
			}
		})
	}
	samples := fresh()
	// Unsorted data, exactly at both bounds. Nearest-rank p95 is sample 48;
	// two slow frames are permitted, three must fail as checked above.
	samples[0].frame.at = base.Add(time.Second)
	samples[10].frame.at = base.Add(time.Second)
	samples[20].frame.at = base.Add(500 * time.Millisecond)
	p95, maximum, err := panelLatencySummary(samples, budget)
	if err != nil || p95 != 500*time.Millisecond || maximum != time.Second {
		t.Fatalf("boundary: p95=%s max=%s err=%v", p95, maximum, err)
	}
}
