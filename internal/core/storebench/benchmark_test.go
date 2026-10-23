// SPDX-License-Identifier: Apache-2.0
package storebench

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"reflect"
	"testing"
)

var turnsFlag = flag.Int("storebench-turns", 100, "generated turns per benchmark corpus")
var snapshotFlag = flag.String("storebench-hermes", "", "offline Hermes LCM SQLite snapshot (read-only)")

func TestGeneratedCorpus(t *testing.T) {
	a := generate(100)
	if !reflect.DeepEqual(a, generate(100)) {
		t.Fatal("corpus is not reproducible")
	}
	s := a.stats()
	if s.Turns != 100 || s.Events != 1830 || s.PersonEvents != 100 || s.HangulEvents < 460 || s.HangulEvents > 490 || s.LargeEvents < 25 || s.LargeEvents > 28 || s.TextBytes < 3800000 || s.TextBytes > 4300000 || s.Summaries == 0 {
		t.Fatalf("unexpected numeric shape: %+v", s)
	}
}

func TestSmallCorpusReport(t *testing.T) {
	r, err := measure(t.Context(), t.TempDir(), generate(4))
	if err != nil {
		t.Fatal(err)
	}
	if r.Corpus.Turns != 4 || r.Corpus.PersonEvents != 4 || r.MemoryItems != 4 || r.Corpus.Summaries == 0 {
		t.Fatalf("counts: %+v", r.Corpus)
	}
	for name, d := range r.Latency {
		if d.Count == 0 || d.P50NS > d.P95NS || d.P95NS > d.MaxNS {
			t.Fatalf("invalid distribution: %s", name)
		}
	}
	for _, name := range []string{"admit_turn", "remember", "publish_summary", "ledger_search", "memory_recall", "recall_person", "forget"} {
		if r.Latency[name].Count == 0 {
			t.Fatalf("missing metric: %s", name)
		}
	}
	for _, table := range []string{"conv_event", "memory_item", "memory_revision", "sum_node"} {
		if r.TableBytes[table] == 0 {
			t.Fatalf("missing dbstat table: %s", table)
		}
	}
	if r.Hits["ledger_search"] == 0 || r.Hits["memory_recall"] == 0 || r.Hits["recall_person"] == 0 || r.ForgetHidden != 3 {
		t.Fatal("paths were not exercised")
	}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var shape any
	if err := json.Unmarshal(data, &shape); err != nil {
		t.Fatal(err)
	}
	checkNumeric(t, shape)
}

func checkNumeric(t *testing.T, value any) {
	t.Helper()
	switch v := value.(type) {
	case float64:
	case map[string]any:
		for _, child := range v {
			checkNumeric(t, child)
		}
	default:
		t.Fatal("report contains a nonnumeric value")
	}
}

func BenchmarkStore(b *testing.B) {
	var c corpus
	if *snapshotFlag != "" {
		var err error
		c, err = readSnapshot(b.Context(), *snapshotFlag)
		if err != nil {
			b.Fatal("storebench_snapshot_invalid")
		}
	} else {
		c = generate(*turnsFlag)
	}
	if len(c.Turns) == 0 {
		b.Fatal("storebench_empty_corpus")
	}
	b.ResetTimer()
	var r report
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		root, err := os.MkdirTemp("", "lina-storebench-")
		if err != nil {
			b.Fatal("storebench_scratch_failed")
		}
		b.StartTimer()
		r, err = measure(b.Context(), root, c)
		b.StopTimer()
		cleanupErr := os.RemoveAll(root)
		if err != nil || cleanupErr != nil {
			b.Fatal("storebench_measurement_failed")
		}
	}
	b.ReportMetric(float64(r.IndexBytes), "index-B")
	b.ReportMetric(float64(r.Latency["admit_turn"].P50NS), "admit-p50-ns/turn")
	b.ReportMetric(float64(r.Latency["recall_person"].P95NS), "recall-person-p95-ns")
	data, err := json.Marshal(r)
	if err != nil {
		b.Fatal("storebench_report_failed")
	}
	fmt.Fprintln(os.Stdout, string(data))
}
