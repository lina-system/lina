// SPDX-License-Identifier: Apache-2.0
package install

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDiscoverGeminiUsesACPInitializeVersion(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprintf("configured=%t", configured), func(t *testing.T) {
			bin := t.TempDir()
			path := filepath.Join(bin, "gemini")
			// A real child handles the protocol; --version intentionally differs.
			quoted := "'" + strings.ReplaceAll(executable, "'", "'\"'\"'") + "'"
			write(t, path, []byte("#!/bin/sh\n# SPDX-License-Identifier: Apache-2.0\nexec "+quoted+" -test.run=^TestGeminiDiscoveryProcess$ -- \"$@\"\n"), 0700)
			t.Setenv("LN753_GEMINI_DISCOVERY_PROCESS", "1")
			t.Setenv("LINA_RESPONSES_API_KEY", "synthetic-core-secret")
			cfg := Config{}
			if configured {
				cfg.ToolPaths = map[string]string{"gemini": path}
				t.Setenv("PATH", t.TempDir())
			} else {
				t.Setenv("PATH", bin)
			}
			host := NewProcessHost()
			t.Cleanup(func() { _ = host.Close(context.Background()) })
			tools := discover(t.Context(), cfg, host, nil)
			i := slices.IndexFunc(tools, func(tool Tool) bool { return tool.Name == "gemini" })
			if i < 0 {
				t.Fatal("Gemini CLI is absent from the installed-tool inventory")
			}
			tool := tools[i]
			if tool.Path != path || tool.Version != "0.65.0" || tool.Status != "unverified" {
				t.Fatalf("Gemini CLI must record initialize.agentInfo.version: %+v", tool)
			}
		})
	}
}

// TestGeminiDiscoveryProcess is a credential-free native Gemini ACP fixture.
func TestGeminiDiscoveryProcess(t *testing.T) {
	if os.Getenv("LN753_GEMINI_DISCOVERY_PROCESS") != "1" {
		return
	}
	if os.Getenv("LINA_RESPONSES_API_KEY") != "" {
		os.Exit(91)
	}
	separator := slices.Index(os.Args, "--")
	if separator < 0 || len(os.Args[separator+1:]) != 1 {
		os.Exit(92)
	}
	if os.Args[separator+1] == "--version" {
		fmt.Println("version-output-is-not-the-handshake")
		os.Exit(0)
	}
	if os.Args[separator+1] != "--acp" {
		os.Exit(93)
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				ProtocolVersion int `json:"protocolVersion"`
			} `json:"params"`
		}
		if decoder.Decode(&request) != nil {
			os.Exit(0)
		}
		if request.Method != "initialize" || request.Params.ProtocolVersion != 1 {
			os.Exit(94)
		}
		if err := encoder.Encode(map[string]any{
			"jsonrpc": "2.0", "id": request.ID,
			"result": map[string]any{
				"protocolVersion":   1,
				"agentCapabilities": map[string]any{"loadSession": true},
				"agentInfo":         map[string]string{"name": "gemini-cli", "version": "0.65.0"},
				"authMethods":       []any{},
			},
		}); err != nil {
			os.Exit(95)
		}
	}
}
