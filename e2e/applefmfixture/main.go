// SPDX-License-Identifier: Apache-2.0

// Command applefmfixture is the on-device helper stand-in for headless tests.
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	var req struct {
		ID, Op, Model, Input string
		Schema               json.RawMessage
	}
	if json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&req) != nil || req.ID == "" {
		os.Exit(1)
	}
	note(req.Op)
	if req.Model == "apple-foundation-private-cloud" {
		exit(map[string]any{"id": req.ID, "ok": false, "unavailable": true})
	}
	if req.Op == "profile" {
		exit(map[string]any{"id": req.ID, "ok": true, "profile": map[string]any{
			"context_tokens": 4096, "tool_calling": true, "guided_generation": true,
			"image_input": false, "model": req.Model, "private_cloud": false,
		}})
	}
	if strings.Contains(req.Input, "force-down") {
		exit(map[string]any{"id": req.ID, "ok": false, "unavailable": true})
	}
	if strings.Contains(req.Input, "force-miss") {
		exit(map[string]any{"id": req.ID, "ok": false, "schema_ok": false, "object": json.RawMessage(`{"title":"guess"}`)})
	}
	if len(req.Schema) > 0 {
		exit(map[string]any{"id": req.ID, "ok": true, "schema_ok": true, "object": json.RawMessage(`{"title":"draft","date":"2026-10-24","time":"15:00"}`)})
	}
	exit(map[string]any{"id": req.ID, "ok": true, "text": "Safari"})
}

func note(op string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(exe), "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, _ = f.WriteString(op + "\n")
	_ = f.Close()
}

func exit(body any) {
	if json.NewEncoder(os.Stdout).Encode(body) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
