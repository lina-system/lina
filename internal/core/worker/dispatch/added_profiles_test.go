// SPDX-License-Identifier: Apache-2.0
package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/lease"
)

func writeAddedProfiles(t *testing.T, root, body string) {
	t.Helper()
	file := filepath.Join(root, "system", "core", "worker-profiles.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestAddedACPFactoriesAndServiceDeclaration(t *testing.T) {
	d := definition(t)
	root, bin := t.TempDir(), t.TempDir()
	command := filepath.Join(bin, "synthetic-agent")
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 98\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	writeAddedProfiles(t, root, `{"version":1,"profiles":[{"name":"added","command":"synthetic-agent","args":["acp"]},{"name":"missing","command":"missing-command"}]}`)
	factories, problems := AddedACPFactories(Config{Root: root})
	if len(problems) != 1 || !strings.Contains(problems[0].Error(), "command is unavailable") || !slices.Equal(factoryNames(factories), []string{"added"}) {
		t.Fatal(factories, problems)
	}
	l := lease.New(func() {})
	defer l.Release()
	adapter, close, err := factories[0].Open(t.Context(), l)
	if err != nil {
		t.Fatal(err)
	}
	defer close(context.Background())
	if adapter.Name() != "added" {
		t.Fatal("wrong adapter", adapter.Name())
	}
	if version, err := adapter.Version(t.Context()); err != nil || version != "unknown" {
		t.Fatal("added profile ran a version command", version, err)
	}
	s := &Service{cfg: Config{Workers: factories}}
	var declaration struct {
		Parameters struct {
			Properties map[string]struct{ Enum []string }
		}
	}
	if err := json.Unmarshal(s.Declaration(), &declaration); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(declaration.Parameters.Properties["worker"].Enum, "added") || slices.Contains(declaration.Parameters.Properties["worker"].Enum, "missing") {
		t.Fatal("declaration differs from loaded profiles", declaration)
	}
	d.Worker = "added"
	if err := validate(d, factories...); err != nil {
		t.Fatal("added worker refused by admission", err)
	}
	if validate(d) == nil {
		t.Fatal("another Core inherited the added worker")
	}
	if !s.WorkerAvailable("added") || !slices.Equal(factoryNames(s.workersFor(Record{})), []string{"added"}) || !slices.Equal(factoryNames(s.workersFor(Record{Definition: d})), []string{"added"}) {
		t.Fatal("added worker cannot be selected")
	}
}

func TestAddedProfilesReserveEveryBuiltinEvenWhenAbsent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	for _, f := range workerRegistry() {
		raw, _ := json.Marshal(map[string]any{"version": 1, "profiles": []any{map[string]any{"name": f.Name, "command": "not-installed"}}})
		writeAddedProfiles(t, root, string(raw))
		factories, problems := AddedACPFactories(Config{Root: root})
		if len(factories) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Error(), "name is reserved") {
			t.Fatalf("builtin %s not reserved: %v %v", f.Name, factories, problems)
		}
	}
}

func TestAddedProfilesOptionalAndBadFile(t *testing.T) {
	root := t.TempDir()
	if factories, problems := AddedACPFactories(Config{Root: root}); len(factories) != 0 || len(problems) != 0 {
		t.Fatal("missing optional settings refused", factories, problems)
	}
	writeAddedProfiles(t, root, "{")
	if factories, problems := AddedACPFactories(Config{Root: root}); len(factories) != 0 || len(problems) != 1 || !strings.Contains(problems[0].Error(), "invalid JSON") {
		t.Fatal("bad settings not reported", factories, problems)
	}
	writeAddedProfiles(t, root, strings.Repeat(" ", 64<<10+1))
	if factories, problems := AddedACPFactories(Config{Root: root}); len(factories) != 0 || len(problems) != 1 {
		t.Fatal("oversized settings accepted", factories, problems)
	}
}
