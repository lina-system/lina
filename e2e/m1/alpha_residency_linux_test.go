// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

const alphaResidentBudget = 256 << 20 // Declared in evidence/v2-residency/budget.json.

func TestAlphaResidency24Hours(t *testing.T) {
	if os.Getenv("LINA_ALPHA_RESIDENCY") != "24h" {
		t.Skip("explicit 24-hour acceptance only")
	}
	requireRealModel(t)
	if os.Getenv("LINA_RESPONSES_MODEL") != "claude-opus-5-5" {
		t.Fatal("alpha residency requires the owner-selected model")
	}
	alphaResidency(t, 24*time.Hour)
}

func TestAlphaResidencyProbe(t *testing.T) {
	if os.Getenv("LINA_ALPHA_RESIDENCY") != "probe" {
		t.Skip("explicit residency collector preflight only")
	}
	if probes := alphaResidency(t, 65*time.Second); probes < 3 {
		t.Fatal("residency preflight did not exercise its periodic health probe")
	}
}

type alphaResidentSample struct {
	Elapsed float64 `json:"elapsed_seconds"`
	RSS     uint64  `json:"rss_bytes"`
	Peak    uint64  `json:"peak_rss_bytes"`
	Start   string  `json:"process_start_ticks"`
}

// alphaResidentSampleGap is the declared maximum gap between resource samples
// (evidence/v2-residency/budget.json). It is not a persistence deadline.
const alphaResidentSampleGap = 2 * time.Second

// alphaResidencyRecorder samples Core's counters and persists them apart from
// the sampling path. A filesystem that stalls longer than the sample interval
// must appear as write latency, never as a sampling gap: the collector's own
// fsync is not evidence about Core.
type alphaResidencyRecorder struct {
	read    func() (alphaResidentSample, error)
	now     func() time.Time
	persist func([]alphaResidentSample) error

	mu        sync.Mutex
	pending   []alphaResidentSample
	samples   int
	persisted int
	gap       float64
	write     time.Duration
	last      time.Time
	start     string
	peak      uint64
	measured  float64
	failure   error
}

type alphaResidencyMetrics struct {
	Samples      int
	Persisted    int
	MaximumGap   float64
	MaximumWrite float64
	Peak         uint64
	Measured     float64
}

func (r *alphaResidencyRecorder) sample() error {
	reading, err := r.read()
	if err != nil {
		return err
	}
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.start == "" {
		r.start = reading.Start
	} else if reading.Start != r.start {
		r.failure = errors.New("Core process identity changed")
	}
	if r.samples > 0 {
		gap := now.Sub(r.last).Seconds()
		r.gap = max(r.gap, gap)
		if gap > alphaResidentSampleGap.Seconds() {
			r.failure = fmt.Errorf("resource sample gap %.3fs exceeds %.0fs", gap, alphaResidentSampleGap.Seconds())
		}
	}
	r.last = now
	r.samples++
	r.peak = max(r.peak, reading.Peak)
	r.measured = reading.Elapsed
	r.pending = append(r.pending, reading)
	return r.failure
}

// flush persists the samples taken since the previous flush and records how
// long that took, so a later sampling failure is attributable.
func (r *alphaResidencyRecorder) flush() error {
	r.mu.Lock()
	batch := r.pending
	r.pending = nil
	r.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}
	begin := r.now()
	err := r.persist(batch)
	took := r.now().Sub(begin)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.write = max(r.write, took)
	if err != nil {
		return err
	}
	r.persisted += len(batch)
	return nil
}

func (r *alphaResidencyRecorder) metrics() alphaResidencyMetrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return alphaResidencyMetrics{
		Samples:      r.samples,
		Persisted:    r.persisted,
		MaximumGap:   r.gap,
		MaximumWrite: r.write.Seconds(),
		Peak:         r.peak,
		Measured:     r.measured,
	}
}

func alphaReadResident(pid int) (alphaResidentSample, error) {
	var sample alphaResidentSample
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return sample, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		if fields[0] != "VmRSS:" && fields[0] != "VmHWM:" {
			continue
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return sample, err
		}
		if fields[0] == "VmRSS:" {
			sample.RSS = n * 1024
		} else {
			sample.Peak = n * 1024
		}
	}
	data, err = os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return sample, err
	}
	// comm may contain spaces and parentheses. Fields after its final ')' start
	// with state (field 3); starttime is field 22.
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return sample, fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return sample, fmt.Errorf("truncated process stat")
	}
	sample.Start = fields[19]
	if sample.RSS == 0 || sample.Peak < sample.RSS {
		return sample, fmt.Errorf("missing resident counters")
	}
	return sample, nil
}

// A stalled filesystem is the collector's own latency, not a sampling gap: the
// 24-hour run must not report a gap in Core's counters because resident.jsonl
// blocked in fsync. The gap limit and the RSS budget keep their declared values.
func TestAlphaResidencyPersistDoesNotDelaySampling(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	block := make(chan struct{})
	recorder := &alphaResidencyRecorder{
		read: func() (alphaResidentSample, error) {
			return alphaResidentSample{RSS: 1, Peak: 1, Start: "1"}, nil
		},
		now: clock.Now,
		persist: func([]alphaResidentSample) error {
			<-block
			clock.Advance(3 * time.Second)
			return nil
		},
	}
	sampled := make(chan error, 1)
	go func() { sampled <- recorder.sample() }()
	select {
	case err := <-sampled:
		if err != nil {
			close(block)
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		close(block)
		t.Fatal("sampling waited for persistence")
	}
	flushed := make(chan error, 1)
	go func() { flushed <- recorder.flush() }()
	close(block)
	select {
	case err := <-flushed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("flush did not finish")
	}
	m := recorder.metrics()
	if m.Samples != 1 || m.Persisted != 1 || m.MaximumGap != 0 {
		t.Fatalf("metrics=%+v; a three-second write became a sampling gap", m)
	}
	if m.MaximumWrite < 3 {
		t.Fatalf("write latency %.3fs was not recorded", m.MaximumWrite)
	}
}

// A sampler that really was late still fails the declared limit.
func TestAlphaResidencyLateSampleStillFails(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	recorder := &alphaResidencyRecorder{
		read: func() (alphaResidentSample, error) {
			return alphaResidentSample{RSS: 1, Peak: 1, Start: "1"}, nil
		},
		now:     clock.Now,
		persist: func([]alphaResidentSample) error { return nil },
	}
	if err := recorder.sample(); err != nil {
		t.Fatal(err)
	}
	clock.Advance(alphaResidentSampleGap)
	if err := recorder.sample(); err != nil {
		t.Fatalf("a sample at the declared limit was rejected: %v", err)
	}
	clock.Advance(alphaResidentSampleGap + time.Millisecond)
	if err := recorder.sample(); err == nil {
		t.Fatal("a sample past the declared limit was accepted")
	}
}

// A changed process identity is a failed run, not a new baseline.
func TestAlphaResidencyRejectsReplacedCore(t *testing.T) {
	t.Parallel()
	start := "1"
	recorder := &alphaResidencyRecorder{
		read:    func() (alphaResidentSample, error) { return alphaResidentSample{RSS: 1, Peak: 1, Start: start}, nil },
		now:     time.Now,
		persist: func([]alphaResidentSample) error { return nil },
	}
	if err := recorder.sample(); err != nil {
		t.Fatal(err)
	}
	start = "2"
	if err := recorder.sample(); err == nil {
		t.Fatal("a replaced Core process was accepted")
	}
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func alphaResidency(t *testing.T, duration time.Duration) int {
	directory := os.Getenv("LINA_ALPHA_CAPTURES")
	if !filepath.IsAbs(directory) {
		t.Fatal("absolute LINA_ALPHA_CAPTURES directory required")
	}
	directory = filepath.Join(directory, t.Name())
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	samples, err := os.OpenFile(filepath.Join(directory, "resident.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer samples.Close()
	report := struct {
		Status          string    `json:"status"`
		SourceDirty     bool      `json:"source_dirty"`
		SourceCommit    string    `json:"source_commit"`
		ArtifactSHA256  string    `json:"artifact_sha256"`
		Provider        string    `json:"provider"`
		RequiredSeconds int       `json:"required_seconds"`
		MeasuredSeconds float64   `json:"measured_seconds"`
		Budget          uint64    `json:"peak_budget_bytes"`
		Peak            uint64    `json:"peak_rss_bytes"`
		Samples         int       `json:"samples"`
		Persisted       int       `json:"persisted_samples"`
		Probes          int       `json:"health_probes"`
		MaximumGap      float64   `json:"maximum_sample_gap_seconds"`
		MaximumWrite    float64   `json:"maximum_write_seconds"`
		Started         time.Time `json:"started_at"`
		Ended           time.Time `json:"ended_at"`
	}{Status: "incomplete", RequiredSeconds: 86400, Budget: alphaResidentBudget}
	var recorder *alphaResidencyRecorder
	defer func() {
		if t.Failed() {
			report.Status = "failed"
		}
		// A failed run still reports what it measured.
		if recorder != nil {
			m := recorder.metrics()
			report.Samples, report.Persisted = m.Samples, m.Persisted
			report.MaximumGap, report.MaximumWrite = m.MaximumGap, m.MaximumWrite
			report.Peak, report.MeasuredSeconds = max(report.Peak, m.Peak), m.Measured
		}
		report.Ended = time.Now().UTC()
		raw, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(directory, "result.json"), append(raw, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}()
	source, err := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	report.SourceCommit = strings.TrimSpace(string(source))
	dirty, err := exec.CommandContext(t.Context(), "git", "status", "--porcelain").Output()
	if err != nil {
		t.Fatal(err)
	}
	report.SourceDirty = len(dirty) != 0
	if duration >= 24*time.Hour && report.SourceDirty {
		t.Fatal("24-hour acceptance requires a clean source commit")
	}

	h, start := alphaOwnerHarness(t, false)
	report.Provider = "claude-opus-5-5"
	binary, err := os.ReadFile(h.binary)
	if err != nil {
		t.Fatal(err)
	}
	report.ArtifactSHA256 = fmt.Sprintf("%x", sha256.Sum256(binary))
	ctx, cancel := context.WithTimeout(t.Context(), duration+10*time.Minute)
	defer cancel()
	h.ctx = ctx
	core := start()
	stopped := false
	defer func() {
		if !stopped {
			h.stopCore(core)
		}
	}()
	var mainID, epoch string
	probe := func() error {
		ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
		defer cancel()
		c, err := clientapi.DialWithCapabilities(ctx, h.socket, "alpha-residency", []protocol.Capability{{Name: "conversation", Version: "1.0"}})
		if err != nil {
			return fmt.Errorf("residency handshake failed: %w", err)
		}
		defer c.Close()
		if mainID != "" && (c.Welcome.MainID != mainID || c.Welcome.Epoch != epoch) {
			return fmt.Errorf("Core identity or epoch changed during residency")
		}
		// Only the initial synchronous probe initializes this identity.
		if mainID == "" {
			mainID, epoch = c.Welcome.MainID, c.Welcome.Epoch
		}
		id, err := c.Input(ctx, protocol.Input{})
		if err != nil {
			return err
		}
		frame, err := c.Receive(ctx)
		if err != nil {
			return err
		}
		if frame.ReplyTo == nil || *frame.ReplyTo != id || frame.Error == nil || frame.Error.Code != protocol.ErrorMalformed {
			return fmt.Errorf("Core did not answer the correlated readiness probe")
		}
		return nil
	}
	if err := probe(); err != nil {
		t.Fatal(err)
	}
	report.Probes++

	observer, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	alphaOwnerTurn(t, h, terminal, observer, "24시간 상주 시험을 시작할게. 짧게 인사해 줘.", "resident-start", "")
	observer.Close()
	begin := time.Now()
	report.Started = begin.UTC()
	encoder := json.NewEncoder(samples)
	recorder = &alphaResidencyRecorder{
		now: time.Now,
		read: func() (alphaResidentSample, error) {
			reading, err := alphaReadResident(core.command.Process.Pid)
			if err != nil {
				return reading, err
			}
			reading.Elapsed = time.Since(begin).Seconds()
			return reading, nil
		},
		persist: func(batch []alphaResidentSample) error {
			for _, reading := range batch {
				if err := encoder.Encode(reading); err != nil {
					return err
				}
			}
			return samples.Sync()
		},
	}
	sample := func() {
		select {
		case <-core.done:
			t.Fatal("Core exited during residency")
		default:
		}
		if err := recorder.sample(); err != nil {
			t.Fatal(err)
		}
		if m := recorder.metrics(); m.Peak > alphaResidentBudget {
			t.Fatalf("Core peak RSS %d exceeds predeclared budget %d", m.Peak, alphaResidentBudget)
		}
	}
	sample()
	// Persistence runs on its own goroutine so neither the sample interval nor
	// a health probe waits for the filesystem.
	flushCtx, stopFlush := context.WithCancel(t.Context())
	flushed := make(chan struct{})
	flushResult := make(chan error, 1)
	// Every exit path stops the writer before the sample file closes.
	defer func() { stopFlush(); <-flushed }()
	go func() {
		defer close(flushed)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-flushCtx.Done():
				return
			case <-ticker.C:
				if err := recorder.flush(); err != nil {
					flushResult <- err
					return
				}
			}
		}
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	health := time.NewTicker(time.Minute)
	defer health.Stop()
	// A slow-but-valid health reply must not suspend the RSS sampler.
	healthResult := make(chan error, 1)
	var healthWorkers sync.WaitGroup
	defer healthWorkers.Wait()
	probing := false
	finishProbe := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
		report.Probes++
	}
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case <-ticker.C:
			sample()
		case <-health.C:
			if probing {
				t.Fatal("overlapping residency health probes")
			}
			probing = true
			healthWorkers.Add(1)
			go func() { defer healthWorkers.Done(); healthResult <- probe() }()
		case err := <-healthResult:
			probing = false
			finishProbe(err)
		case err := <-flushResult:
			t.Fatal(err)
		case <-core.done:
			t.Fatal("Core exited during residency")
		case <-terminal.process.done:
			t.Fatal("TUI exited during residency")
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-deadline.C:
			sample()
			stopFlush()
			<-flushed
			select {
			case err := <-flushResult:
				t.Fatal(err)
			default:
			}
			if err := recorder.flush(); err != nil {
				t.Fatal(err)
			}
			if probing {
				finishProbe(<-healthResult)
				probing = false
			}
			finishProbe(probe())
			alphaScreen(t, h, terminal, "resident-end", func(s string) bool { return strings.Contains(s, "연결됨") }, "")
			h.keys(terminal, "\x04")
			terminal.process.wait(t, 0)
			h.stopCore(core)
			stopped = true
			// Decide on the metrics themselves: the deferred report writer runs
			// after this return and must not be the only source of the duration.
			if duration >= 24*time.Hour && recorder.metrics().Measured >= 86400 {
				report.Status = "passed"
			}
			return report.Probes
		}
	}
}
