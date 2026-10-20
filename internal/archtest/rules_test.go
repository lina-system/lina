// SPDX-License-Identifier: Apache-2.0
package archtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/thisisjun786/lina"

func writeSource(t *testing.T, root, name, source string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRules(t *testing.T) {
	for _, tc := range []struct {
		name, file, source, want string
	}{
		{"TUI excludes Core", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/internal/core/boot"`, "TUI"},
		{"TUI excludes providers", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/kit/provider/responses"`, "TUI"},
		{"TUI excludes third party", "internal/tui/state/file.go", `package state; import _ "example.com/client"`, "TUI"},
		{"TUI near prefix", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/internal/clientapis"`, "TUI"},
		{"TUI typed client", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/internal/clientapi"`, ""},
		{"TUI protocol", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/kit/protocol"`, ""},
		{"TUI design", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/internal/design"`, ""},
		{"TUI sibling", "internal/tui/ui/file.go", `package ui; import _ "` + module + `/internal/tui/state"`, ""},
		{"TUI standard library", "internal/tui/ui/file.go", `package ui; import _ "fmt"`, ""},
		{"upward layer", "internal/core/canon/file.go", `package canon; import _ "` + module + `/internal/core/authority"`, "layer"},
		{"L2 upward", "internal/core/authority/file.go", `package authority; import _ "` + module + `/internal/core/ledger"`, "layer"},
		{"L3 upward", "internal/core/ledger/file.go", `package ledger; import _ "` + module + `/internal/core/lane"`, "layer"},
		{"L4 upward", "internal/core/lane/file.go", `package lane; import _ "` + module + `/internal/core/boot"`, "layer"},
		{"peer stores", "internal/core/agents/file.go", `package agents; import _ "` + module + `/internal/core/memory"`, "layer"},
		{"peer flows", "internal/core/lane/file.go", `package lane; import _ "` + module + `/internal/core/turn"`, "layer"},
		{"unknown core", "internal/core/surprise/file.go", `package surprise`, "unmapped"},
		{"unknown core descendant", "internal/core/surprise/nested/file.go", `package nested`, "unmapped"},
		{"core root", "internal/core/file.go", `package core`, "unmapped"},
		{"unknown imported core", "internal/core/boot/file.go", `package boot; import _ "` + module + `/internal/core/surprise"`, "unmapped"},
		{"kit to root", "kit/protocol/file.go", `package protocol; import _ "` + module + `"`, "kit"},
		{"kit to core", "kit/protocol/file.go", `package protocol; import _ "` + module + `/internal/core/ids"`, "kit"},
		{"Charm outside UI", "internal/core/boot/file.go", `package boot; import _ "github.com/charmbracelet/bubbletea"`, "Charm"},
		{"Charm new module", "internal/tui/file.go", `package tui; import _ "charm.land/bubbletea/v2"`, "Charm"},
		{"Charm near prefix", "internal/tui/uikit/file.go", `package uikit; import _ "github.com/charmbracelet/lipgloss"`, "Charm"},
		{"exec outside wrapper", "internal/core/exec/file.go", `package exec; import _ "os/exec"`, "os/exec"},
		{"exec near prefix", "kit/process/file.go", `package process; import _ "os/exec"`, "os/exec"},
		{"Environ", "kit/sandbox/file.go", `package sandbox; import "os"; var env = os.Environ()`, "os.Environ"},
		{"aliased Environ", "kit/tools/file.go", `package tools; import system "os"; var env = system.Environ`, "os.Environ"},
		{"dot Environ", "kit/tools/file.go", `package tools; import . "os"; var env = Environ()`, "os.Environ"},
		{"Node forbidden kit", "internal/node/file.go", `package node; import _ "` + module + `/kit/provider/responses"`, "Node"},
		{"Node root module", "cmd/lina-node/file.go", `package main; import _ "` + module + `/internal/core/ids"`, "Node"},
		{"Node third party", "internal/node/file.go", `package node; import _ "example.com/rpc"`, "Node"},
		{"Node kit root", "internal/node/file.go", `package node; import _ "` + module + `/kit"`, "Node"},
		{"Node kit near prefix", "internal/node/file.go", `package node; import _ "` + module + `/kit/protocols"`, "Node"},
		{"Node version metadata", "cmd/lina-node/file.go", `package main; import _ "` + module + `/internal/buildinfo"`, ""},
		{"Node runtime excludes metadata", "internal/node/file.go", `package node; import _ "` + module + `/internal/buildinfo"`, "Node"},
		{"Node metadata near prefix", "cmd/lina-node/file.go", `package main; import _ "` + module + `/internal/buildinformation"`, "Node"},
		{"Node metadata subpackage", "cmd/lina-node/file.go", `package main; import _ "` + module + `/internal/buildinfo/extra"`, "Node"},
		{"buildinfo standard library", "internal/buildinfo/file.go", `package buildinfo; import _ "fmt"`, ""},
		{"buildinfo excludes core", "internal/buildinfo/file.go", `package buildinfo; import _ "` + module + `/internal/core/ids"`, "buildinfo"},
		{"buildinfo excludes kit", "internal/buildinfo/file.go", `package buildinfo; import _ "` + module + `/kit/protocol"`, "buildinfo"},
		{"buildinfo excludes third party", "internal/buildinfo/file.go", `package buildinfo; import _ "example.com/version"`, "buildinfo"},
		{"kit excludes buildinfo", "kit/protocol/file.go", `package protocol; import _ "` + module + `/internal/buildinfo"`, "kit"},
		{"inactive platform", "internal/core/canon/file_windows.go", "//go:build windows\n\npackage canon; import _ \"os/exec\"", "os/exec"},
		{"downward", "internal/core/turn/file.go", `package turn; import _ "` + module + `/internal/core/agents"`, ""},
		{"boot composes flows", "internal/core/boot/file.go", `package boot; import _ "` + module + `/internal/core/turn"`, ""},
		{"package descendants", "internal/core/canon/file.go", `package canon; import _ "` + module + `/internal/core/canon/migrations"`, ""},
		{"kit sibling", "kit/tools/file.go", `package tools; import _ "` + module + `/kit/protocol"`, ""},
		{"module near prefix", "kit/tools/file.go", `package tools; import _ "` + module + `-extra"`, ""},
		{"terminal Charm", "internal/tui/term/file.go", `package term; import _ "github.com/charmbracelet/x/term"`, ""},
		{"UI Charm", "internal/tui/ui/widget/file.go", `package widget; import _ "charm.land/lipgloss/v2"`, ""},
		{"proc", "kit/proc/file.go", `package proc; import ("os"; "os/exec"); var env = os.Environ`, ""},
		{"sandbox exec", "kit/sandbox/file.go", `package sandbox; import _ "os/exec"`, ""},
		{"os other call", "internal/core/boot/file.go", `package boot; import "os"; var value = os.Getenv("PATH")`, ""},
		{"shadowed os", "kit/tools/file.go", `package tools; import "os"; func f() { os := struct { Environ func() }{}; os.Environ() }`, ""},
		{"shadowed dot name", "kit/tools/file.go", `package tools; import . "os"; func f() { Environ := func(){}; Environ() }`, ""},
		{"unrelated selector", "kit/tools/file.go", `package tools; import . "os"; func f(other T) { other.Environ() }`, ""},
		{"source text is not code", "kit/tools/file.go", "package tools\n// os.Environ()\nvar text = `os.Environ()`", ""},
		{"test harness", "cmd/lina/main_test.go", `package main; import "os/exec"`, ""},
		{"testdata", "internal/core/testdata/bad/file.go", `package bad; import "os/exec"`, ""},
		{"vendor", "vendor/example.com/tool/file.go", `package tool; import "os/exec"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeSource(t, root, tc.file, tc.source)
			got, err := violations(root, map[string]bool{"os": true, "os/exec": true, "fmt": true})
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected violations: %v", got)
				}
			} else if len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), tc.want) {
				t.Fatalf("violations = %v, want %q", got, tc.want)
			}
		})
	}
}

func TestNodeAllowlist(t *testing.T) {
	for _, owner := range []string{"internal/node", "cmd/lina-node"} {
		for _, pkg := range []string{"grant", "lease", "sandbox", "proc", "protocol", "rpc", "diag", "secret"} {
			t.Run(owner+"/"+pkg, func(t *testing.T) {
				root := t.TempDir()
				writeSource(t, root, owner+"/file.go", fmt.Sprintf("package node; import (_ %q; _ %q)", module+"/kit/"+pkg, "fmt"))
				got, err := violations(root, map[string]bool{"fmt": true})
				if err != nil || len(got) != 0 {
					t.Fatalf("allowed Node import: %v, %v", got, err)
				}
			})
		}
	}
}

func TestMalformedSourceFails(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "internal/core/boot/file.go", "package boot; import")
	if _, err := violations(root, nil); err == nil {
		t.Fatal("malformed Go source passed the scan")
	}
}

func TestSeededImportFailsGoTest(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "go.mod", "module "+module+"\n\ngo 1.27\n")
	for _, name := range []string{"rules.go", "imports_test.go"} {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		writeSource(t, root, "internal/archtest/"+name, string(source))
	}
	writeSource(t, root, "internal/core/canon/seed.go", "package canon\nimport _ \"os/exec\"\n")
	command := exec.Command("go", "test", "./internal/archtest/...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=readonly")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "internal/core/canon/seed.go:2") || !strings.Contains(string(output), "os/exec") {
		t.Fatalf("seeded import must fail go test with its location and rule: %v\n%s", err, output)
	}
	writeSource(t, root, "internal/core/canon/seed.go", "package canon\n")
	clean := exec.Command("go", "test", "./internal/archtest/...")
	clean.Dir, clean.Env = command.Dir, command.Env
	if output, err := clean.CombinedOutput(); err != nil {
		t.Fatalf("removing the seeded import must restore a passing go test: %v\n%s", err, output)
	}
}
