// SPDX-License-Identifier: Apache-2.0
package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thisisjun786/lina/internal/clientapi"
	"github.com/thisisjun786/lina/kit/protocol"
)

// TestM3WorkerLanesLiveSession additionally starts the installed stdio worker
// fixture through Core's conversation tools, makes it write a real artifact,
// and expands its runtime lane to show the worker output without calling the
// model. While the worker holds an interrupt reply, the developer page must
// return its session as unknown when the five-second snapshot context expires.
// Only then does the fixture release the interrupt, produce its result and
// allow the refreshed lane to report idle. Vendor transport is synthetic; no
// personal account is used.
//
// The same m3 scenario also covers the live worker registry introduced by #133:
// conversation tool -> installed stdio worker -> real artifact -> lane detail.
func TestM3WorkerLanesLiveSession(t *testing.T) {
	t.Parallel()

	var scenario struct{ Command, LiveInput, LiveResult string }
	raw, err := os.ReadFile("../m3/worker-lanes.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &scenario); err != nil {
		t.Fatal(err)
	}
	workdir := t.TempDir()
	var calls atomic.Int32
	var control map[string]any
	server := httptest.NewServer(toolCatalogModel(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Input, Tools []json.RawMessage }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if scriptedCodingManager(t, w, req) {
			return
		}
		if respondWorkerCompletion(w, req.Input, scenario.LiveResult) {
			return
		}
		n := calls.Add(1)
		if n > 1 {
			var item struct{ Output string }
			json.Unmarshal(req.Input[len(req.Input)-1], &item)
			var out struct {
				Code   string
				Output json.RawMessage
			}
			json.Unmarshal([]byte(item.Output), &out)
			var result map[string]any
			if out.Code != "ok" || json.Unmarshal(out.Output, &result) != nil {
				t.Errorf("worker tool failed: %s", item.Output)
				emitResponse(w, "worker failed", true)
				return
			}
			control = map[string]any{"session": result["session"], "turn": result["turn"], "generation": result["generation"], "binding": result["binding"]}
			if n == 6 && result["output"] != scenario.LiveResult {
				t.Errorf("worker output=%v", result["output"])
			}
		}
		emit := func(name string, args map[string]any) { emitPersonaCall(w, fmt.Sprintf("lane-call-%d", n), name, args) }
		switch n {
		case 1:
			emit("worker_start", map[string]any{"workdir": workdir, "task_packet": "hold for input"})
		case 2:
			control["quote"] = scenario.LiveInput
			emit("worker_interrupt", control)
		case 3:
			emit("worker_snapshot", map[string]any{"session": control["session"]})
		case 4:
			control["text"] = "produce result"
			emit("worker_input", control)
		case 5:
			emit("worker_snapshot", map[string]any{"session": control["session"]})
		default:
			emitResponse(w, "Worker produced the file.", true)
		}
	})))
	defer server.Close()
	h, bootstrap := installedConversationHarness(t, server.URL)
	seedCodingFixture(t, h, bootstrap)
	// The fixture acknowledges interrupt arrival, then withholds its RPC reply
	// until Core returns a developer page. The adapter must remain locked while
	// the page's five-second snapshot context expires.
	laneResult := make(chan error, 1)
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		readLane := func() error {
			client, err := clientapi.DialWithCapabilities(ctx, h.socket, "lane-deadline", []protocol.Capability{{Name: "worker_lanes", Version: "1.0"}})
			if err != nil {
				return err
			}
			defer client.Close()
			if _, err = client.Progress(ctx, protocol.ProgressGet{}); err != nil {
				return err
			}
			frame, err := client.Receive(ctx)
			if err != nil {
				return err
			}
			p, ok := frame.Payload.(protocol.Progress)
			if frame.Error != nil || !ok || p.Lanes == nil || len(*p.Lanes) != 1 {
				return fmt.Errorf("missing lane while interrupt held: %+v", frame)
			}
			lane := (*p.Lanes)[0]
			if lane.State != "unknown" || lane.ThreadID != "fixture-thread" || lane.TurnID != "turn-1" || lane.TaskID == "" || lane.Generation == "0" {
				return fmt.Errorf("expired snapshot lost state or identity: %+v", lane)
			}
			return nil
		}
		laneResult <- readLane()
	}))
	defer gate.Close()
	bin := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	core := h.command("core", "--state-root", h.root, "--socket", h.socket)
	core.command.Env = append(core.command.Env, "PATH="+bin, "LINA_WORKER_FIXTURE=1", "CODEX_HOME=/fixture/codex-home", "LINA_RESPONSES_BASE_URL="+server.URL+"/v1", "LINA_RESPONSES_MODEL=fixture-model", "LINA_RESPONSES_API_KEY=synthetic-key")
	core.command.Env = append(core.command.Env, "LINA_WORKER_INTERRUPT_GATE="+gate.URL)
	core.start(t)
	h.connect(core).Close()
	c, conversation := conversationClient(t, h)
	admittedTurn(t, h, c, conversation, "lane-worker", scenario.LiveInput)
	select {
	case err := <-laneResult:
		if err != nil {
			t.Fatalf("developer lane did not return unknown during held interrupt: %v", err)
		}
	default:
		t.Fatal("worker interrupt did not reach the snapshot deadline scenario")
	}
	receiveWorkerFollowups(t, h, c, 2)
	if calls.Load() != 6 {
		t.Fatalf("worker rounds=%d", calls.Load())
	}
	artifact, err := os.ReadFile(filepath.Join(workdir, "result.txt"))
	if err != nil || string(artifact) != scenario.LiveResult {
		t.Fatalf("worker artifact=%q %v", artifact, err)
	}
	c.Close()
	socket := h.socket
	tap := newAccountTap(t, h, socket)
	h.socket = tap.socket
	terminal := h.startTerminalSize([]string{"TERM=xterm-256color"}, false, true, 80, 35)
	waitComposerReady(t, h, terminal)
	h.keys(terminal, scenario.Command+"\r")
	for {
		e := tap.next(t, h)
		if e.Error != nil {
			t.Fatal(e.Error)
		}
		if p, ok := e.Payload.(protocol.Progress); ok {
			if p.Lanes == nil || len(*p.Lanes) != 1 || (*p.Lanes)[0].State != "idle" || (*p.Lanes)[0].ThreadID != "fixture-thread" {
				t.Fatalf("live worker not projected: %+v", p)
			}
			break
		}
	}
	folded := waitScreen(t, h, terminal, "[codex]")
	if strings.Contains(screenText(folded), "fixture-thread") {
		t.Fatal("live worker unfolded by default")
	}
	h.keys(terminal, "\r")
	waitScreen(t, h, terminal, "fixture-thread")
	waitScreen(t, h, terminal, scenario.LiveResult)
	if calls.Load() != 6 {
		t.Fatal("developer view called the model")
	}
	h.keys(terminal, "\x04")
	terminal.process.wait(t, 0)
	h.socket = socket
	h.stopCore(core)
}
