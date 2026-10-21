// SPDX-License-Identifier: Apache-2.0

package run

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/tui/spike"
)

func TestVerdictsRequireMeasurements(t *testing.T) {
	for _, id := range scenarioIDs {
		if _, err := verdict(id, map[string]any{}); err == nil {
			t.Errorf("%s accepted an unrun scenario", id)
		}
	}
	for _, tc := range []struct {
		id     string
		values map[string]any
		want   string
	}{
		{"S2", map[string]any{"max_tick_bytes": float64(2048), "columns": float64(140), "rows": float64(45)}, "pass"},
		{"S2", map[string]any{"max_tick_bytes": float64(2049), "columns": float64(140), "rows": float64(45)}, "fail"},
		{"S7", map[string]any{"later_key_worked": false, "observed_ms": float64(1000), "mitigation": "Bound the paste buffer and reset the decoder after a deadline."}, "gap"},
		{"S7", map[string]any{"later_key_worked": true, "observed_ms": float64(999)}, "pass"},
		{"S8", map[string]any{"runs": true, "quits": true, "parses": true, "sleep_calls": float64(0)}, "pass"},
		{"S9", map[string]any{"cpu_percent_one_core": float64(1), "idle_bytes": float64(0), "peak_resident_mib": float64(80), "window_items": float64(200)}, "pass"},
		{"S9", map[string]any{"cpu_percent_one_core": float64(0), "idle_bytes": float64(1), "peak_resident_mib": float64(10), "window_items": float64(200)}, "fail"},
	} {
		got, err := verdict(tc.id, tc.values)
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q, %v; want %q", tc.id, got, err, tc.want)
		}
	}
	for _, value := range []any{nil, "unmeasured", math.NaN(), math.Inf(1), float64(-1)} {
		if _, err := verdict("S2", map[string]any{"max_tick_bytes": value, "columns": float64(140), "rows": float64(45)}); err == nil {
			t.Errorf("accepted invalid measurement %v", value)
		}
	}
}

func TestEvidenceCannotOverwriteOrInventMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s8.json")
	local := false
	r := spike.Record{Format: 1, Scenario: "S8", Terminal: spike.Terminal{Name: "fixture", Version: "1", TERM: "xterm", SSH: &local}, Libraries: fixturePins(), Bounds: rawMetrics(boundsFor("S8")), Measured: rawMetrics(map[string]any{"runs": true, "quits": true, "parses": true, "sleep_calls": float64(0)}), Result: "pass"}
	if err := saveRecord(path, r); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := saveRecord(path, r); err == nil {
		t.Fatal("overwrote existing evidence")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("existing evidence changed")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := spike.ReadRecord(f); err != nil {
		t.Fatal(err)
	}
	r.Terminal.Version = ""
	if err := saveRecord(filepath.Join(t.TempDir(), "bad.json"), r); err == nil {
		t.Fatal("saved missing terminal version")
	}
}

func fixturePins() map[string]string {
	return map[string]string{"charm.land/bubbletea/v2": "v2.1.0", "charm.land/bubbles/v2": "v2.2.1", "charm.land/lipgloss/v2": "v2.0.6", "charm.land/glamour/v2": "v2.0.1", "github.com/charmbracelet/ultraviolet": "v0.0.0-20261008173134-6b8d4baf91b4", "github.com/charmbracelet/x/vt": "v0.1.0"}
}

func TestPercentileAndFixtures(t *testing.T) {
	if got := percentile([]float64{4, 1, 3, 2}, .95); got != 4 {
		t.Fatal(got)
	}
	fixtures := streamFixtures()
	if len(fixtures) != 6 || len(fixtures[0].text) != 100000 || len(fixtures[1].text) != 300000 || len(fixtures[2].text) != 300000 {
		t.Fatal("stream fixture sizes differ from declared workload")
	}
	for _, f := range fixtures {
		if f.id == "" || f.text == "" {
			t.Fatal("empty fixture")
		}
	}
	var encoded map[string]any
	data, err := json.Marshal(boundsFor("S3"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &encoded); err != nil {
		t.Fatal(err)
	}
	if encoded["events_per_second"] != float64(250) || encoded["peak_heap_mib"] != float64(64) {
		t.Fatal("streaming bounds changed")
	}
}
