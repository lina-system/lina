// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/json"
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
	Elapsed       float64  `json:"elapsed_seconds"`
	RSS           uint64   `json:"rss_bytes"`
	Peak          uint64   `json:"peak_rss_bytes"`
	Start         string   `json:"process_start_ticks"`
	Gap           float64  `json:"gap_seconds"`
	Read          float64  `json:"read_seconds"`
	PreviousRead  float64  `json:"previous_read_seconds"`
	PreviousWrite float64  `json:"previous_write_seconds"`
	PreviousSync  float64  `json:"previous_sync_seconds"`
	MemorySome    *float64 `json:"memory_some_avg10,omitempty"`
	MemoryFull    *float64 `json:"memory_full_avg10,omitempty"`
	IOSome        *float64 `json:"io_some_avg10,omitempty"`
	IOFull        *float64 `json:"io_full_avg10,omitempty"`
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

func alphaPressureAvg10(kind string) (some, full *float64) {
	text, err := os.ReadFile("/proc/pressure/" + kind)
	if err != nil {
		return nil, nil
	}
	someValue, fullValue, ok := parsePressureAvg10(string(text))
	if !ok {
		return nil, nil
	}
	return &someValue, &fullValue
}

func alphaProductCommit(binary string) string {
	if directory := os.Getenv("LINA_ALPHA_BUILD"); filepath.IsAbs(directory) {
		var manifest struct {
			Commit string `json:"source_commit"`
		}
		raw, err := os.ReadFile(filepath.Join(directory, "pinned-binaries.json"))
		if err == nil && json.Unmarshal(raw, &manifest) == nil && len(manifest.Commit) == 40 {
			return manifest.Commit
		}
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return ""
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
		Probes          int       `json:"health_probes"`
		MaximumGap      float64   `json:"maximum_sample_gap_seconds"`
		ReadBeforeGap   float64   `json:"read_seconds_before_maximum_gap"`
		WriteBeforeGap  float64   `json:"write_seconds_before_maximum_gap"`
		SyncBeforeGap   float64   `json:"sync_seconds_before_maximum_gap"`
		MaximumRead     float64   `json:"maximum_read_seconds"`
		MaximumWrite    float64   `json:"maximum_write_seconds"`
		MaximumSync     float64   `json:"maximum_sync_seconds"`
		ProductCommit   string    `json:"product_commit"`
		Started         time.Time `json:"started_at"`
		Ended           time.Time `json:"ended_at"`
	}{Status: "incomplete", RequiredSeconds: 86400, Budget: alphaResidentBudget}
	defer func() {
		if t.Failed() {
			report.Status = "failed"
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
	report.ProductCommit = alphaProductCommit(h.binary)
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
	var gaps residencyGapRecord
	sample := func() {
		select {
		case <-core.done:
			t.Fatal("Core exited during residency")
		default:
		}
		now := time.Now()
		readStart := time.Now()
		reading, err := alphaReadResident(core.command.Process.Pid)
		if err != nil {
			t.Fatal(err)
		}
		reading.MemorySome, reading.MemoryFull = alphaPressureAvg10("memory")
		reading.IOSome, reading.IOFull = alphaPressureAvg10("io")
		readSeconds := time.Since(readStart).Seconds()
		reading.Elapsed = now.Sub(begin).Seconds()
		report.MeasuredSeconds = reading.Elapsed
		gap := now.Sub(last).Seconds()
		last = now
		reading.Gap = gap
		reading.Read = readSeconds
		reading.PreviousRead = gaps.read
		reading.PreviousWrite = gaps.write
		reading.PreviousSync = gaps.sync
		if reading.Start != first.Start {
			t.Fatal("Core process identity changed")
		}
		report.Peak = max(report.Peak, reading.Peak)
		report.Samples++
		raw, err := json.Marshal(reading)
		if err != nil {
			t.Fatal(err)
		}
		writeStart := time.Now()
		if _, err := samples.Write(append(raw, '\n')); err != nil {
			t.Fatal(err)
		}
		writeSeconds := time.Since(writeStart).Seconds()
		// File.Sync is fsync. Its duration stays on the next gap so a stall
		// can be told apart from the scheduler without moving the limit.
		syncStart := time.Now()
		if err := samples.Sync(); err != nil {
			t.Fatal(err)
		}
		syncSeconds := time.Since(syncStart).Seconds()
		gaps.note(gap, readSeconds, writeSeconds, syncSeconds)
		report.MaximumGap = gaps.Maximum
		report.ReadBeforeGap = gaps.ReadBeforeMaximum
		report.WriteBeforeGap = gaps.WriteBeforeMaximum
		report.SyncBeforeGap = gaps.SyncBeforeMaximum
		report.MaximumRead = max(report.MaximumRead, readSeconds)
		report.MaximumWrite = max(report.MaximumWrite, writeSeconds)
		report.MaximumSync = max(report.MaximumSync, syncSeconds)
		if gap > 2 {
			t.Fatalf("resource sample gap %.3fs exceeds two seconds; previous read %.3fs write %.3fs sync %.3fs", gap, gaps.ReadBeforeMaximum, gaps.WriteBeforeMaximum, gaps.SyncBeforeMaximum)
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
