// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2ClaudeAdapterThroughInstalledCore(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../m2/claude-adapter.json")
	if err != nil {
		t.Fatal(err)
	}
	var scenario struct{ Input, Reply string }
	if err := json.Unmarshal(data, &scenario); err != nil {
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	program, err := filepath.Abs("../m2/claude-runtime.mjs")
	if err != nil {
		t.Fatal(err)
	}
	h, _ := installedConversationHarness(t, "")
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "LINA_CLAUDE_NODE="+node, "LINA_CLAUDE_ADAPTER="+program, "LINA_CLAUDE_MODEL=fixture-model")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		return core
	}
	core := start()
	client, conversation := conversationClient(t, h)
	events := admittedTurn(t, h, client, conversation, "claude-input", scenario.Input)
	var final, delta bool
	for _, event := range events {
		if item, ok := event.Payload.(protocol.ItemDone); ok && item.Text != nil && *item.Text == scenario.Reply {
			final = true
		}
		if event.Kind == protocol.KindOutputDelta {
			delta = true
		}
	}
	if !final || !delta {
		t.Fatalf("adapter did not complete tool-backed reply: final=%t delta=%t events=%+v", final, delta, events)
	}
	client.Close()
	h.stopCore(core)
	core = start()
	again, _ := conversationClient(t, h)
	topic := "conversation:" + conversation
	if _, err := again.Subscribe(h.ctx, protocol.Subscribe{Topic: &topic}); err != nil {
		t.Fatal(err)
	}
	event, err := again.Receive(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(event.Payload)
	if !strings.Contains(string(raw), scenario.Reply) {
		t.Fatalf("reply did not survive restart: %s", raw)
	}
	again.Close()
	h.stopCore(core)
}
