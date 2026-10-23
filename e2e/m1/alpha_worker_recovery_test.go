// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

func TestAlphaWorkerCrashInstalledCore(t *testing.T) {
	t.Parallel()
	workdir := t.TempDir()
	var launches atomic.Int32
	provider := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid provider input")
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		var last struct{ Type, Output string }
		if len(req.Input) > 0 {
			json.Unmarshal(req.Input[len(req.Input)-1], &last)
		}
		if last.Type == "function_call_output" {
			var result struct {
				Code   string
				Output struct{ State string }
			}
			if err := json.Unmarshal([]byte(last.Output), &result); err != nil || result.Code != "ok" || result.Output.State != "active" {
				t.Errorf("worker_start did not start an active worker: %s (%v)", last.Output, err)
			}
			emitResponse(w, "작업을 맡겼어요.", true)
		} else {
			launches.Add(1)
			emitPersonaCall(w, "start-crash-worker", "worker_start", map[string]any{"workdir": workdir, "task_packet": "hold for input"})
		}
	})))
	defer provider.Close()
	h, bootstrap := installedConversationHarness(t, provider.URL)
	seedCodingFixture(t, h, bootstrap)
	bin := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(executable, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	start := func() *process {
		core := h.command("core", "--state-root", h.root, "--socket", h.socket)
		core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+provider.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
		core.start(t)
		h.connect(core).Close()
		waitCoreReady(t, h)
		return core
	}
	lanes := func() []protocol.WorkerLane {
		c, err := clientapi.DialWithCapabilities(h.ctx, h.socket, "alpha-recovery", []protocol.Capability{{Name: "worker_lanes", Version: "1.0"}})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		id, err := c.Progress(h.ctx, protocol.ProgressGet{})
		if err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := c.Receive(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if frame.ReplyTo == nil || *frame.ReplyTo != id {
				continue
			}
			p, ok := frame.Payload.(protocol.Progress)
			if !ok || p.Lanes == nil {
				t.Fatal("missing lane snapshot")
			}
			return *p.Lanes
		}
	}
	core := start()
	c, _ := conversationClient(t, h)
	terminal, _ := h.startConversationTUI(120, 34, "TERM=xterm-256color", "LC_ALL=ko")
	alphaScreen(t, h, terminal, "fresh", func(s string) bool { return strings.Contains(s, "Ctrl+D") }, "")
	alphaTurn(t, h, terminal, c, "재시작 복구를 확인할 작업을 맡겨 줘.", "작업을 맡겼어요.", "running")
	before := lanes()
	if len(before) != 1 || before[0].ThreadID != "fixture-thread" || before[0].State != "active" {
		t.Fatalf("no observed active worker before crash: %+v", before)
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	c.Close()
	core.killAndWait() // SIGKILL, after an acknowledged active-thread snapshot.
	core = start()
	defer h.stopCore(core)
	after := lanes()
	if len(after) != 1 || after[0].ThreadID != before[0].ThreadID || after[0].TurnID != before[0].TurnID {
		t.Fatalf("Core restart lost the interrupted worker: before=%d after=%d", len(before), len(after))
	}
	if launches.Load() != 1 {
		t.Fatal("recovery dispatched the original job again")
	}
	if after[0].State == "active" {
		t.Fatal("recovery claimed live execution without fresh worker evidence")
	}
}
