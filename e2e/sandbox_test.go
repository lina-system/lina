// SPDX-License-Identifier: Apache-2.0

package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The real Core/TUI remains reachable when confinement cannot run. Conversation
// bodies are still reserved by the protocol; the harness tests the live client
// service and its existing correlated refusal, not a fabricated model reply.
func TestM1SandboxUnavailable(t *testing.T) {
	data, err := os.ReadFile("m1/sandbox.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Cases []struct {
			Name, Cause, OS string
			LauncherExit    int `json:"launcher_exit"`
		}
		ConnectionState string `json:"connection_state"`
		DiagnosticCode  string `json:"diagnostic_code"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.OS != "" && tc.OS != runtime.GOOS {
				t.Skip("platform scenario")
			}
			h := newHarness(t, binary)
			control := filepath.Join(h.root, "system/core")
			if err := os.MkdirAll(control, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(control, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
				t.Fatal(err)
			}
			core := h.command("core", "--state-root", h.root, "--socket", h.socket)
			if tc.LauncherExit != 0 {
				bin := filepath.Join(h.root, "bin")
				if err := os.Mkdir(bin, 0700); err != nil {
					t.Fatal(err)
				}
				// A failed executable, not a fake success standing in for isolation.
				if err := os.WriteFile(filepath.Join(bin, "bwrap"), []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexit "+strconv.Itoa(tc.LauncherExit)+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
				for i, entry := range core.command.Env {
					if strings.HasPrefix(entry, "PATH=") {
						core.command.Env[i] = "PATH=" + bin
					}
				}
			}
			core.start(t)
			client := h.client(core)
			term := h.startTUI()
			h.expectState(term, scenario.ConnectionState)
			h.inputRefused(client)
			h.keys(term, "q\n")
			term.process.wait(t, 0)
			h.inputRefused(h.client(core))
			h.stopCore(core)
			found := false
			for _, line := range strings.Split(core.stderr.String(), "\n") {
				code, cause, ok := strings.Cut(line, ": ")
				if ok && code == scenario.DiagnosticCode && cause == tc.Cause {
					found = true
				}
			}
			if !found {
				t.Fatalf("missing sandbox failure code %s: %s", tc.Cause, &core.stderr)
			}
		})
	}
}
