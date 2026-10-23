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

// TestAlphaResidencyWriterKeepsSamplesOffTheWritePath pins that a stalled
// persistence step cannot delay reading Core's counters: real /proc samples
// keep queueing while the writer is blocked inside its first flush.
func TestAlphaResidencyWriterKeepsSamplesOffTheWritePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resident.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	stalled := func(raw []byte) error {
		once.Do(func() { close(entered) })
		<-release
		_, err := file.Write(raw)
		return err
	}
	writer := newAlphaResidentWriter(file, 10*time.Millisecond, stalled)
	writer.add(alphaResidentSample{Elapsed: 1})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("writer did not persist the first sample")
	}
	pid := os.Getpid()
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for i := range 20 {
			reading, err := alphaReadResident(pid)
			if err != nil {
				t.Error(err)
				return
			}
			reading.Elapsed = float64(i)
			writer.add(reading)
		}
	}()
	select {
	case <-collected:
	case <-time.After(5 * time.Second):
		t.Fatal("sampling blocked on a stalled write")
	}
	close(release)
	if err := writer.settle(); err != nil {
		t.Fatal(err)
	}
	if writer.maximumWrite() <= 0 {
		t.Fatal("write time was not recorded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 21 {
		t.Fatalf("persisted %d samples, want 21", lines)
	}
}

// TestAlphaResidencyWriterReportsFailedWrite pins that a persistence failure
// surfaces to the sampling loop instead of silently dropping samples.
func TestAlphaResidencyWriterReportsFailedWrite(t *testing.T) {
	writer := newAlphaResidentWriter(nil, time.Hour, func([]byte) error { return errors.New("disk full") })
	writer.add(alphaResidentSample{Elapsed: 1})
	if err := writer.settle(); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("settle error = %v, want the write failure", err)
	}
	if writer.failure() == nil {
		t.Fatal("write failure was not recorded for the sampler")
	}
	if writer.maximumWrite() <= 0 {
		t.Fatal("write time was not recorded")
	}
}

// alphaResidentWriter persists samples away from the sampling path, so a slow
// filesystem cannot delay the next read of Core's counters.
type alphaResidentWriter struct {
	persist func([]byte) error
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	pending []alphaResidentSample
	maximum time.Duration
	err     error
}

func newAlphaResidentWriter(file *os.File, interval time.Duration, persist func([]byte) error) *alphaResidentWriter {
	writer := &alphaResidentWriter{persist: persist, stop: make(chan struct{}), done: make(chan struct{})}
	go writer.run(interval)
	return writer
}

// alphaPersistSamples writes one batch and flushes it to stable storage.
func alphaPersistSamples(file *os.File) func([]byte) error {
	return func(batch []byte) error {
		if _, err := file.Write(batch); err != nil {
			return err
		}
		return file.Sync()
	}
}

// add queues a sample. It never waits for persistence.
func (writer *alphaResidentWriter) add(sample alphaResidentSample) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.pending = append(writer.pending, sample)
}

func (writer *alphaResidentWriter) run(interval time.Duration) {
	defer close(writer.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			writer.flush()
		case <-writer.stop:
			writer.flush()
			return
		}
	}
}

func (writer *alphaResidentWriter) flush() {
	writer.mu.Lock()
	batch := writer.pending
	writer.pending = nil
	writer.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	var raw []byte
	for _, sample := range batch {
		line, err := json.Marshal(sample)
		if err != nil {
			writer.record(0, err)
			return
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	begin := time.Now()
	err := writer.persist(raw)
	writer.record(time.Since(begin), err)
}

func (writer *alphaResidentWriter) record(elapsed time.Duration, err error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	writer.maximum = max(writer.maximum, elapsed)
	if err != nil && writer.err == nil {
		writer.err = err
	}
}

// settle stops the writer after persisting every queued sample.
func (writer *alphaResidentWriter) settle() error {
	close(writer.stop)
	<-writer.done
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.err
}

func (writer *alphaResidentWriter) maximumWrite() time.Duration {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.maximum
}

func (writer *alphaResidentWriter) failure() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.err
}

type alphaResidentSample struct {
	Elapsed float64 `json:"elapsed_seconds"`
	RSS     uint64  `json:"rss_bytes"`
	Peak    uint64  `json:"peak_rss_bytes"`
	Start   string  `json:"process_start_ticks"`
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
	writer := newAlphaResidentWriter(samples, time.Second, alphaPersistSamples(samples))
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
		Probes          int       `json:"health_probes"`
		MaximumGap      float64   `json:"maximum_sample_gap_seconds"`
		MaximumWrite    float64   `json:"maximum_write_seconds"`
		Started         time.Time `json:"started_at"`
		Ended           time.Time `json:"ended_at"`
	}{Status: "incomplete", RequiredSeconds: 86400, Budget: alphaResidentBudget}
	// Settling persists the last queued samples before the report reads the
	// recorded write time, so both artifacts describe the same run.
	defer func() {
		if err := writer.settle(); err != nil {
			t.Error(err)
		}
		if err := samples.Close(); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			report.Status = "failed"
		}
		report.MaximumWrite = writer.maximumWrite().Seconds()
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
	first, err := alphaReadResident(core.command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}

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
	last := begin
	sample := func() {
		select {
		case <-core.done:
			t.Fatal("Core exited during residency")
		default:
		}
		now := time.Now()
		reading, err := alphaReadResident(core.command.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		reading.Elapsed = now.Sub(begin).Seconds()
		report.MeasuredSeconds = reading.Elapsed
		gap := now.Sub(last).Seconds()
		report.MaximumGap = max(report.MaximumGap, gap)
		last = now
		if reading.Start != first.Start {
			t.Fatal("Core process identity changed")
		}
		report.Peak = max(report.Peak, reading.Peak)
		report.Samples++
		// Queueing is in-memory; the writer flushes and fsyncs on its own path.
		writer.add(reading)
		if err := writer.failure(); err != nil {
			t.Fatalf("persisting resident samples: %v", err)
		}
		if gap > 2 {
			t.Fatal("resource sample gap exceeds two seconds")
		}
		if reading.Peak > alphaResidentBudget {
			t.Fatalf("Core peak RSS %d exceeds predeclared budget %d", reading.Peak, alphaResidentBudget)
		}
	}
	sample()
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
		case <-core.done:
			t.Fatal("Core exited during residency")
		case <-terminal.process.done:
			t.Fatal("TUI exited during residency")
		case <-h.ctx.Done():
			t.Fatal(h.ctx.Err())
		case <-deadline.C:
			sample()
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
			if duration >= 24*time.Hour && report.MeasuredSeconds >= 86400 {
				report.Status = "passed"
			}
			return report.Probes
		}
	}
}
