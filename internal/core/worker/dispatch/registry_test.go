// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/thisisjun786/lina/internal/core/worker"
	"github.com/thisisjun786/lina/kit/lease"
)

func factoryNames(factories []Factory) []string {
	names := make([]string, 0, len(factories))
	for _, f := range factories {
		names = append(names, f.Name)
	}
	return names
}

func TestOpenRegistersBinaryWorkers(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "binary_only", true: "configured_chooser"}[configured], func(t *testing.T) {
			cfg := Config{DB: store(t), Binary: "/fixture/agy", ClaudeBinary: "/fixture/claude"}
			wantChoice := []string{"antigravity"}
			if configured {
				cfg.Workers = []Factory{CodexFactory("/fixture/codex")}
				wantChoice = []string{"codex"}
			}
			s, err := Open(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close(context.Background())
			for _, name := range []string{"antigravity", "claude"} {
				if !slices.Contains(factoryNames(s.cfg.Workers), name) {
					t.Errorf("binary worker %s missing from registry", name)
				}
				if !s.WorkerAvailable(name) || !slices.Equal(factoryNames(s.workersFor(Record{Definition: Definition{Worker: name}})), []string{name}) {
					t.Errorf("registered worker %s unavailable for explicit selection", name)
				}
			}
			if got := factoryNames(s.workersFor(Record{})); !slices.Equal(got, wantChoice) {
				t.Fatalf("chooser order changed: got %v want %v", got, wantChoice)
			}
		})
	}
}

func TestWorkerLookupUsesOnlyRegistry(t *testing.T) {
	s := &Service{cfg: Config{Binary: "/fixture/agy", ClaudeBinary: "/fixture/claude"}}
	for _, name := range []string{"antigravity", "claude", "missing"} {
		if s.WorkerAvailable(name) {
			t.Errorf("unregistered worker %s reported available", name)
		}
		if got := s.workersFor(Record{Definition: Definition{Worker: name}}); len(got) != 0 {
			t.Errorf("unregistered worker %s manufactured a factory", name)
		}
	}
}

func TestRegistrySelectionFlagsAndReturns(t *testing.T) {
	s := &Service{cfg: Config{Workers: []Factory{ClaudeFactory(Config{}), CodexFactory(""), AntigravityFactory(Config{})}}}
	for _, tc := range []struct {
		name   string
		record Record
		want   []string
	}{
		{"chooser", Record{}, []string{"codex", "antigravity"}},
		{"explicit", Record{Definition: Definition{Worker: "claude"}}, []string{"claude"}},
		{"not_explicit", Record{Definition: Definition{Worker: "codex"}}, nil},
		{"return_chooser_worker", Record{Worker: "codex", ReturnReceipt: "receipt"}, []string{"codex"}},
		{"return_overrides_definition", Record{Definition: Definition{Worker: "claude"}, Worker: "antigravity", ReturnReceipt: "receipt"}, []string{"antigravity"}},
		{"missing_return_worker", Record{Worker: "missing", ReturnReceipt: "receipt"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := factoryNames(s.workersFor(tc.record)); !slices.Equal(got, tc.want) {
				t.Fatalf("selected %v want %v", got, tc.want)
			}
		})
	}
	s.cfg.Workers = []Factory{{Name: "antigravity", Explicit: true, Chooser: true}}
	if s.WorkerAvailable("antigravity") || len(s.workersFor(Record{})) != 0 || len(s.workersFor(Record{Definition: Definition{Worker: "antigravity"}})) != 0 {
		t.Fatal("factory without an opener became runnable")
	}
}

func TestUnavailableSelectionIsNotAQuotaWait(t *testing.T) {
	s := &Service{cfg: Config{DB: store(t)}}
	l := lease.New(func() {})
	defer l.Release()
	for _, r := range []Record{{Definition: Definition{Worker: "claude"}}, {Worker: "codex", ReturnReceipt: "receipt"}} {
		if a, close, err := s.selectWorker(t.Context(), l, r); err == nil || a != nil || close != nil {
			t.Fatalf("missing worker became a quota wait: %v %v", a, err)
		}
	}
}

func TestOpenKeepsConfiguredFactory(t *testing.T) {
	f := AntigravityFactory(Config{Binary: "/configured/agy"})
	want := &quotaAdapter{name: f.Name}
	f.Open = func(context.Context, lease.Lease) (worker.Adapter, func(context.Context) error, error) {
		return want, func(context.Context) error { return nil }, nil
	}
	s, err := Open(t.Context(), Config{DB: store(t), Binary: "/fallback/agy", Workers: []Factory{f}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if got := factoryNames(s.cfg.Workers); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("binary duplicated configured registration: %v", got)
	}
	if got := factoryNames(s.workersFor(Record{})); !slices.Equal(got, []string{"antigravity"}) {
		t.Fatalf("binary changed configured chooser eligibility: %v", got)
	}
	l := lease.New(func() {})
	defer l.Release()
	a, close, err := s.selectWorker(t.Context(), l, Record{})
	if err != nil || a != want {
		t.Fatalf("binary replaced configured opener: %v %v", a, err)
	}
	defer close(context.Background())
}

func TestRegistryPreservesAdmissionAndRuleSets(t *testing.T) {
	wantExplicit := []string{"antigravity", "claude", "claude-acp", "cursor", "gemini", "grok"}
	var declaration struct {
		Parameters struct {
			Properties map[string]struct{ Enum []string }
		}
	}
	if err := json.Unmarshal(Declaration(), &declaration); err != nil {
		t.Fatal(err)
	}
	if got := declaration.Parameters.Properties["worker"].Enum; !slices.Equal(got, wantExplicit) {
		t.Fatalf("explicit selection changed: %v", got)
	}
	d := definition(t)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "system", "core"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range append(append([]string{"", "codex", "missing"}, wantExplicit...), "CODEX") {
		t.Run(name, func(t *testing.T) {
			d.Worker = name
			if got, want := validate(d) == nil, name == "" || slices.Contains(wantExplicit, name); got != want {
				t.Errorf("admission for %q: accepted=%v want %v", name, got, want)
			}
			raw, _ := json.Marshal(map[string]any{"provisional": true, "rules": []map[string]any{{"worker": name, "priority": 1}}})
			if err := os.WriteFile(filepath.Join(root, "system", "core", "worker-choice.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := ReadRules(root)
			if got, want := err == nil, name == "antigravity" || name == "codex"; got != want {
				t.Errorf("rule for %q: accepted=%v want %v", name, got, want)
			}
		})
	}
}
