// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRunnable(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
		want   bool
	}{
		{name: "Test", prefix: "Test", want: true},
		{name: "TestFoo", prefix: "Test", want: true},
		{name: "Test_x", prefix: "Test", want: true},
		{name: "Testfoo", prefix: "Test", want: false},
		{name: "Example", prefix: "Test", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runnable(test.name, test.prefix); got != test.want {
				t.Fatalf("runnable(%q, %q) = %v, want %v", test.name, test.prefix, got, test.want)
			}
		})
	}
}

func TestSorted(t *testing.T) {
	empty := sorted(map[string]bool{})
	if empty == nil {
		t.Fatal("empty map returned a nil slice")
	}
	if len(empty) != 0 {
		t.Fatalf("empty map returned %q", empty)
	}
	got := sorted(map[string]bool{"b": true, "a": true, "c": true})
	want := []string{"a", "b", "c"}
	if !slices.Equal(got, want) {
		t.Fatalf("sorted = %q, want %q", got, want)
	}
}

func TestScanRejectsEscapingPath(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"/tmp/x_test.go", "..", "../x_test.go"} {
		t.Run(name, func(t *testing.T) {
			_, err := scan(root, name, "example/pkg")
			if err == nil || !strings.Contains(err.Error(), "scenario source escapes repository") {
				t.Fatalf("scan(%q) error = %v", name, err)
			}
		})
	}
}

func TestScanSupervisorGuard(t *testing.T) {
	const procImport = `"github.com/thisisjun786/lina/kit/proc"`
	dispatches := func(osName, procName string) string {
		return "\tif len(" + osName + ".Args) > 1 && " + osName + ".Args[1] == \"proc-supervise\" {\n" +
			"\t\t" + osName + ".Exit(" + procName + ".SuperviseMain(" + osName + ".Args[2:], " + osName + ".Stderr))\n" +
			"\t}\n" +
			"\tif len(" + osName + ".Args) > 1 && " + osName + ".Args[1] == \"proc-job\" {\n" +
			"\t\t" + osName + ".Exit(" + procName + ".JobMain(" + osName + ".Args[2:], " + osName + ".Stderr))\n" +
			"\t}\n"
	}
	guarded := func(osName, procName string) string {
		return "func TestMain(m *testing.M) {\n" + dispatches(osName, procName) +
			"\t" + osName + ".Exit(m.Run())\n" +
			"}\n"
	}
	tests := []struct {
		name    string
		source  string
		guarded bool
	}{
		{
			name: "direct",
			source: "package fixture_test\nimport (\n\t\"os\"\n\t\"testing\"\n\t" + procImport + "\n)\n" +
				guarded("os", "proc"),
			guarded: true,
		},
		{
			name: "aliases",
			source: "package fixture_test\nimport (\n\tosAlias \"os\"\n\t\"testing\"\n\tprocAlias " + procImport + "\n)\n" +
				guarded("osAlias", "procAlias"),
			guarded: true,
		},
		{
			// A Linux-only dispatch leaves macOS jobs to run the test suite.
			name: "supervisor only",
			source: "package fixture_test\nimport (\n\t\"os\"\n\t\"testing\"\n\t" + procImport + "\n)\n" +
				"func TestMain(m *testing.M) {\n" +
				dispatches("os", "proc")[:strings.Index(dispatches("os", "proc"), "\tif len(os.Args) > 1 && os.Args[1] == \"proc-job\"")] +
				"\tos.Exit(m.Run())\n}\n",
			guarded: false,
		},
		{
			name: "comment",
			source: "package fixture_test\nimport (\n\t\"os\"\n\t\"testing\"\n\t" + procImport + "\n)\n" +
				"func TestMain(m *testing.M) {\n" +
				"\t// os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))\n" +
				"\tos.Exit(m.Run())\n}\n",
			guarded: false,
		},
		{
			name: "string",
			source: "package fixture_test\nimport (\n\t\"os\"\n\t\"testing\"\n\t" + procImport + "\n)\n" +
				"func TestMain(m *testing.M) {\n" +
				"\t_ = \"os.Exit(proc.SuperviseMain(os.Args[2:], os.Stderr))\"\n" +
				"\tos.Exit(m.Run())\n}\n",
			guarded: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			name := "guard_test.go"
			if err := os.WriteFile(filepath.Join(root, name), []byte(test.source), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := scan(root, name, "example/pkg")
			if err != nil {
				t.Fatal(err)
			}
			if got.SupervisorGuard != test.guarded {
				t.Fatalf("SupervisorGuard = %v, want %v", got.SupervisorGuard, test.guarded)
			}
		})
	}
}

func TestRunRequiresGuardForProcImports(t *testing.T) {
	root := t.TempDir()
	rel := "pkg/unprotected_test.go"
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package pkg\nimport (\n\t\"testing\"\n\t\"github.com/thisisjun786/lina/kit/proc\"\n)\n" +
		"func TestUsesProc(t *testing.T) {\n\t_ = proc.SuperviseMain\n}\n"
	if err := os.WriteFile(filepath.Join(root, rel), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(request{
		Root: root,
		Packages: []struct {
			Path  string
			Files []string
		}{{Path: "example/pkg", Files: []string{rel}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(root, "request.json")
	if err := os.WriteFile(requestPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	previous := os.Args
	t.Cleanup(func() { os.Args = previous })
	os.Args = []string{"scenarios", requestPath}
	err = run()
	if err == nil || !strings.Contains(err.Error(), "proc-supervise and proc-job require early TestMain dispatches") {
		t.Fatalf("run() error = %v", err)
	}
}
