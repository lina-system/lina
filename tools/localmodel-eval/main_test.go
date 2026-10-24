// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/core/localmodel"
	"github.com/thisisjun786/lina/kit/lease"
	"github.com/thisisjun786/lina/kit/provider"
	"github.com/thisisjun786/lina/kit/provider/applefm"
)

// The set is the fixed input of a measurement, so a typo in it changes a
// published number. These checks hold the set's shape and the scoring rules
// that read it, not the answers a model gives.
func TestSetLoads(t *testing.T) {
	set, err := loadSet("../../testdata/localmodel-eval/set.json")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, c := range set.Cases {
		counts[c.Kind]++
		if c.input() == "" {
			t.Fatalf("%s: empty input", c.ID)
		}
		if len(c.Schema) > 0 && (len(c.Expect.Fields)+len(c.Expect.FieldAny) == 0) {
			t.Fatalf("%s: structured case checks no field", c.ID)
		}
		if len(c.Schema) == 0 && len(c.Expect.MustInclude)+len(c.Expect.AnyOf) == 0 {
			t.Fatalf("%s: text case checks no content", c.ID)
		}
	}
	for _, kind := range []string{"intent", "autocomplete", "rewrite", "title", "classify", "extract", "summary"} {
		if counts[kind] < 3 {
			t.Fatalf("%s has %d cases; the table needs at least 3", kind, counts[kind])
		}
	}
}

func TestNormalizeDate(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-10-25", "2026-10-25"},
		{"2026년 10월 25일", "2026-10-25"},
		{"2026.11.3", "2026-11-03"},
		{"2026/12/01", "2026-12-01"},
		{"2026-11-03 09:30", "2026-11-03"},
		{"10월 25일", "10월 25일"},
	} {
		if got := normalizeDate(tc.in); got != tc.want {
			t.Errorf("normalizeDate(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeTime(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"15:00", "15:00"},
		{"오후 3시", "15:00"},
		{"오전 10시", "10:00"},
		{"3:00 PM", "15:00"},
		{"09:30", "09:30"},
		{"12:00 오전", "00:00"},
	} {
		if got := normalizeTime(tc.in); got != tc.want {
			t.Errorf("normalizeTime(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

type fakeModel struct {
	provider provider.ProviderID
	ref      provider.ModelRef
	text     string
	err      error
	calls    int
}

func (m *fakeModel) Identity() (provider.ProviderID, provider.ModelRef) {
	return m.provider, m.ref
}

func (m *fakeModel) Complete(_ context.Context, _ lease.Lease, _ localmodel.Call) (localmodel.Completion, error) {
	m.calls++
	if m.err != nil {
		return localmodel.Completion{}, m.err
	}
	return localmodel.Completion{Text: m.text}, nil
}

// A local sample must not be answered by the main model through a schema miss
// or an unavailable helper, and a main sample must not reach the helper. The
// measured table would otherwise credit one path with the other's answer.
func TestColumnsDoNotBorrowTheOtherModel(t *testing.T) {
	missLocal := func() *fakeModel {
		return &fakeModel{provider: applefm.ProviderID, ref: applefm.OnDeviceModel, err: applefm.ErrSchemaMiss}
	}
	answerMain := func() *fakeModel {
		return &fakeModel{provider: provider.ChatGPT, ref: "main-model", text: "the summary"}
	}
	profile := applefm.Profile{Model: applefm.OnDeviceModel, ContextTokens: 4096, GuidedGeneration: true}
	c := Case{ID: "x", Kind: localmodel.Summary, Line: "a short note", Expect: Expect{MustInclude: []string{"summary"}}}

	local, main := missLocal(), answerMain()
	all := localmodel.Deps{Profile: profile, Local: local, Main: main}
	localColumn := runPath(context.Background(), lease.New(func() {}), pathDeps(all, localRoute),
		c, localmodel.ForceLocal, localRoute, 0, 1, 5*time.Second)
	if localColumn.Correct != 0 {
		t.Fatalf("local column counted %d correct runs from the main model", localColumn.Correct)
	}
	if main.calls != 0 {
		t.Fatalf("local column called the main model %d times", main.calls)
	}

	local, main = missLocal(), answerMain()
	all = localmodel.Deps{Profile: profile, Local: local, Main: main}
	mainColumn := runPath(context.Background(), lease.New(func() {}), pathDeps(all, mainRoute),
		c, localmodel.ForceMain, mainRoute, 0, 1, 5*time.Second)
	if mainColumn.Correct != 1 || local.calls != 0 {
		t.Fatalf("main column: correct=%d helper calls=%d", mainColumn.Correct, local.calls)
	}
}

// A run the route sends to the other model is a miss, never a sample.
func TestForeignRouteIsAMiss(t *testing.T) {
	local := &fakeModel{provider: applefm.ProviderID, ref: applefm.OnDeviceModel, err: applefm.ErrSchemaMiss}
	all := localmodel.Deps{
		Profile: applefm.Profile{Model: applefm.OnDeviceModel, ContextTokens: 4096, GuidedGeneration: true},
		Local:   local,
		Main:    &fakeModel{provider: provider.ChatGPT, ref: "main-model", text: "the summary"},
	}
	c := Case{ID: "x", Kind: localmodel.Summary, Line: "a short note", Expect: Expect{MustInclude: []string{"summary"}}}
	out := runPath(context.Background(), lease.New(func() {}), all, c, localmodel.ForceLocal, localRoute, 0, 1, 5*time.Second)
	if out.Correct != 0 || len(out.Failures) != 1 || !strings.Contains(out.Failures[0].Detail, "wanted local") {
		t.Fatalf("foreign route was counted: correct=%d failures=%v", out.Correct, out.Failures)
	}
}

func TestPercentile(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3, 6, 7, 8, 9, 10}
	if got := percentile(values, 0.50); got != 5 {
		t.Errorf("p50=%v want 5", got)
	}
	if got := percentile(values, 0.95); got != 10 {
		t.Errorf("p95=%v want 10", got)
	}
	if got := percentile(nil, 0.5); got != 0 {
		t.Errorf("empty=%v want 0", got)
	}
	if values[0] != 5 {
		t.Errorf("percentile reordered its input")
	}
}
