// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkerToolsMCPFramingAndRefusals(t *testing.T) {
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":"early","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":9007199254740993,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"unknown","method":"resources/list"}`,
		`{"jsonrpc":"2.0","id":"missing","method":"tools/call","params":{"arguments":{}}}`,
		`{"jsonrpc":"2.0","id":"closed","method":"tools/call","params":{"name":"read_projection"}}`,
	}, "\n") + "\n"
	var output bytes.Buffer
	if err := serveWorkerTools(t.Context(), strings.NewReader(input), &output, "/nonexistent/worker.sock", "private-session-token"); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte("\n"))
	ids := []string{`"early"`, `9007199254740993`, `null`, `"unknown"`, `"missing"`, `"closed"`}
	codes := []int{-32600, 0, 0, -32601, -32602, -32000}
	if len(lines) != len(ids) {
		t.Fatal("notification generated a response")
	}
	for i, line := range lines {
		var reply struct {
			JSONRPC    string
			ID, Result json.RawMessage
			Error      *struct{ Code int }
		}
		if err := json.Unmarshal(line, &reply); err != nil {
			t.Fatal(err)
		}
		if reply.JSONRPC != "2.0" || string(reply.ID) != ids[i] {
			t.Fatal("JSON-RPC identity changed")
		}
		if codes[i] == 0 {
			if reply.Error != nil || len(reply.Result) == 0 {
				t.Fatal("missing success")
			}
		} else if reply.Error == nil || reply.Error.Code != codes[i] {
			t.Fatal("wrong refusal")
		}
	}
	if strings.Contains(output.String(), "private-session-token") || strings.Contains(output.String(), "worker.sock") {
		t.Fatal("credentials or socket leaked in MCP output")
	}
}
