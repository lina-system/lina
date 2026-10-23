// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/thisisjun786/lina/kit/protocol"
)

func TestM2ClaudeSilentCancellationThroughInstalledCore(t *testing.T) {
	t.Parallel()
	h, _ := installedConversationHarness(t, "")
	program := filepath.Join(h.root, "silent.sh")
	if err := os.WriteFile(program, []byte("# SPDX-License-Identifier: Apache-2.0\nread -r start\nprintf '%s %s\\n' \"$$\" \"$PPID\" > \"$0.pids\"\nexec /bin/sleep 60\n"), 0600); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "LINA_CLAUDE_NODE=/bin/sh", "LINA_CLAUDE_ADAPTER="+program, "LINA_CLAUDE_MODEL=fixture-model")
	core.start(t)
	h.connect(core).Close()
	waitCoreReady(t, h)
	client, conversation := conversationClient(t, h)
	key, input := "silent-turn", "Start a turn"
	if _, err := client.Input(h.ctx, protocol.Input{ConversationID: &conversation, InputID: &key, Text: &input}); err != nil {
		t.Fatal(err)
	}
	var pid, supervisor int
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		data, err := os.ReadFile(program + ".pids")
		if err == nil {
			if n, _ := fmt.Sscanf(string(data), "%d %d", &pid, &supervisor); n == 2 {
				break
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-h.ctx.Done():
			t.Fatal("Core did not start the silent runtime")
		case <-tick.C:
		}
	}
	// SIGTERM cancels the active model request. Core must join it and exit
	// cleanly, rather than relying on the harness's kill-on-timeout cleanup.
	h.stopCore(core)
	for _, id := range []int{pid, supervisor} {
		if id <= 1 {
			t.Fatalf("invalid child PID %d", id)
		}
		if err := syscall.Kill(id, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("process %d survived Core shutdown: %v", id, err)
		}
	}
}
