// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

// Use the real built Core and the shared process harness. Unsupported versions
// must be constructed as JSON because the generated 1.0 encoder refuses them.
func TestM1ClientListener(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("client-listener.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct {
		Supported   string             `json:"supported_version"`
		Unsupported []string           `json:"unsupported_versions"`
		Refusal     protocol.ErrorCode `json:"refusal_code"`
		Incomplete  string             `json:"incomplete_frame"`
		Independent protocol.ErrorCode `json:"independent_answer_without_capability"`
	}
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	binary := buildLina(t)
	h := newHarness(t, binary)
	core := h.startCore()
	frame := func(version string) []byte {
		data, err := os.ReadFile("../../protocol/fixtures/client/1.0/hello.json")
		if err != nil {
			t.Fatal(err)
		}
		var envelope map[string]any
		if err := json.Unmarshal(data, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope["message_id"] = "handshake-e2e"
		envelope["protocol"].(map[string]any)["version"] = version
		envelope["payload"].(map[string]any)["protocol_versions"] = []string{version}
		envelope["payload"].(map[string]any)["platform"] = runtime.GOOS + "/" + runtime.GOARCH
		data, err = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "handshake-e2e", "method": "hello", "params": envelope})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, version := range scenario.Unsupported {
		t.Run(version, func(t *testing.T) {
			conn := h.connect(core)
			defer conn.Close()
			if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Write(append(frame(version), '\n')); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReaderSize(conn, 1<<20)
			line, err := reader.ReadSlice('\n')
			if err != nil {
				t.Fatal(err)
			}
			answer, err := decodeReply(line, "handshake-e2e")
			if err != nil {
				t.Fatal(err)
			}
			if answer.Kind != protocol.KindError || answer.EffectState != protocol.EffectRefused || answer.Error == nil || answer.Error.Code != scenario.Refusal {
				t.Fatalf("unsupported handshake: %+v", answer)
			}
			if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
				t.Fatalf("refused connection remained open: %v", err)
			}
		})
	}
	t.Run("incomplete frame", func(t *testing.T) {
		if scenario.Incomplete != "close_without_reply" {
			t.Fatal("unrecognized scenario assertion")
		}
		conn := h.connect(core)
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(operationTimeout)); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(frame(scenario.Supported)); err != nil {
			t.Fatal(err)
		}
		if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(conn).ReadByte(); !errors.Is(err, io.EOF) {
			t.Fatalf("incomplete request was dispatched: %v", err)
		}
	})
	// Refusing an individual peer must leave the listener serving supported peers.
	client := h.client(core)
	if client.welcome.ProtocolVersion != scenario.Supported {
		t.Fatalf("selected %s", client.welcome.ProtocolVersion)
	}
	h.inputRefused(client) // Wait for READY through the existing admission probe.
	answer := client.call(t, protocol.KindApprovalAnswer, protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce})
	if answer.Error == nil || answer.Error.Code != scenario.Independent {
		t.Fatalf("independent peer was incorrectly classified as a descendant: %+v", answer.Error)
	}
	h.stopCore(core)
}
