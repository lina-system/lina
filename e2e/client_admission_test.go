// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/thisisjun786/lina/internal/core/boot"
	"github.com/thisisjun786/lina/kit/protocol"
)

type admissionScenario struct {
	Cases []struct {
		Name     string             `json:"name"`
		Versions []string           `json:"versions"`
		Halted   bool               `json:"halted"`
		Refusal  protocol.ErrorCode `json:"refusal"`
	} `json:"cases"`
	DeciderKinds []protocol.Kind `json:"deciderKinds"`
}

func readAdmissionScenario(t *testing.T) admissionScenario {
	t.Helper()
	data, err := os.ReadFile("m1/client-admission.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario admissionScenario
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	return scenario
}

func TestM1ClientAdmission(t *testing.T) {
	scenario := readAdmissionScenario(t)
	binary := buildLina(t)
	for _, tc := range scenario.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			h := newHarness(t, binary)
			if tc.Halted {
				dir := filepath.Join(h.root, "system", "core")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "halt.json"), []byte(`{"reason":"test_halt"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			core := h.startCore()
			conn := h.connect(core)
			c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
			answer := c.call(t, protocol.KindHello, protocol.Hello{Party: protocol.PartyClient, ID: "client-e2e", ProductVersion: "unrelated-product-version", ProtocolVersions: tc.Versions, Capabilities: []protocol.Capability{}})
			if tc.Halted {
				welcome, ok := answer.Payload.(protocol.Welcome)
				if !ok || answer.Kind != protocol.KindWelcome {
					t.Fatalf("recovery handshake: %+v", answer)
				}
				c.welcome = welcome
				answer = c.call(t, protocol.KindInput, protocol.Input{})
			}
			if answer.Error == nil || answer.Error.Code != tc.Refusal || answer.EffectState != protocol.EffectRefused {
				t.Fatalf("refusal: %+v", answer)
			}
			if !tc.Halted {
				if _, err := c.reader.ReadByte(); err != io.EOF {
					t.Fatalf("unsupported connection remains open: %v", err)
				}
			}
			h.stopCore(core)
		})
	}
	t.Run("Core descendant", func(t *testing.T) {
		h := newHarness(t, "")
		dir := filepath.Join(h.root, "system", "core")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.json"), []byte(`{"main_id":"main-e2e"}`), 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(h.ctx)
		defer cancel()
		// Run the production boot path as the ancestor of the synthetic client.
		// The stock command intentionally exposes no child-launch testing switch.
		cfg := boot.Config{StateRoot: h.root, Socket: h.socket, AfterStep: func(step boot.Step, _ boot.Snapshot) error {
			if step != boot.Ready {
				return nil
			}
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestM1DescendantClient$")
			child.Env = []string{"LINA_TEST_DESCENDANT_SOCKET=" + h.socket}
			output, err := child.CombinedOutput()
			cancel()
			if err != nil {
				return fmt.Errorf("descendant: %w\n%s", err, output)
			}
			return nil
		}}
		if err := boot.Run(ctx, cfg); err != nil {
			t.Fatal(err)
		}
	})
}

func TestM1DescendantClient(t *testing.T) {
	socket := os.Getenv("LINA_TEST_DESCENDANT_SOCKET")
	if socket == "" {
		t.Skip("subprocess helper")
	}
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &client{conn: conn, reader: bufio.NewReaderSize(conn, 1<<20)}
	answer := c.call(t, protocol.KindHello, protocol.Hello{Party: protocol.PartyClient, ID: "client-e2e", ProductVersion: "0.0.0-dev", ProtocolVersions: []string{"1.0"}, Capabilities: []protocol.Capability{}})
	welcome, ok := answer.Payload.(protocol.Welcome)
	if !ok {
		t.Fatalf("hello: %+v", answer)
	}
	c.welcome = welcome
	payloads := map[protocol.Kind]protocol.Payload{
		protocol.KindApprovalAnswer: protocol.ApprovalAnswer{Choice: protocol.ApprovalChoiceOnce},
		protocol.KindQuestionAnswer: protocol.QuestionAnswer{Answer: protocol.QuestionAnswerActionDecline},
		protocol.KindWaitingAnswer:  protocol.WaitingAnswer{},
	}
	for _, kind := range readAdmissionScenario(t).DeciderKinds {
		answer := c.call(t, kind, payloads[kind])
		if answer.Error == nil || answer.Error.Code != protocol.ErrorNotAuthorized || answer.EffectState != protocol.EffectRefused {
			t.Fatalf("descendant %s: %+v", kind, answer)
		}
	}
}
